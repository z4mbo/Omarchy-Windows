//go:build windows

package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const seamlessWindowPort = 4457

var errSeamlessCapture = errors.New("window capture unavailable")
var errSeamlessInput = errors.New("window input unavailable")
var errSeamlessGone = errors.New("window gone")

var activeSeamlessBridge atomic.Pointer[seamlessWindowBridge]

// This is a deliberately small, local-only prototype. Capture mode gives the
// guest one Wayland surface per granted Windows HWND. Native mode, explicitly
// enabled for a boot, places those HWNDs directly over QEMU's client area.
// Capture/input forwarding is disabled in native mode; no general game or
// exclusive-fullscreen compatibility is implied.
type seamlessWindowBridge struct {
	token      string
	backend    seamlessWindowBackend
	frames     chan struct{}
	projection *nativeProjection
	mu         sync.Mutex
	grants     map[seamlessWindowKey]string
	pending    []seamlessLaunch
}

type seamlessWindowKey struct {
	pid    uint32
	handle uintptr
}

type seamlessLaunch struct {
	pid      uint32
	until    time.Time
	previous map[seamlessWindowKey]bool
}

type seamlessWindowBackend interface {
	windows() []seamlessWindow
	frame(seamlessWindow) ([]byte, error)
	input(seamlessWindow, seamlessInput) error
	close(seamlessWindow) error
}

type seamlessWindow struct {
	ID         string `json:"id"`
	HWND       string `json:"hwnd"`
	PID        uint32 `json:"pid"`
	Title      string `json:"title"`
	Class      string `json:"class"`
	Process    string `json:"process"`
	AppGroup   string `json:"appGroup"`
	X          int32  `json:"x"`
	Y          int32  `json:"y"`
	Width      int32  `json:"width"`
	Height     int32  `json:"height"`
	Fullscreen bool   `json:"fullscreen"`
	handle     uintptr
}

type seamlessInput struct {
	Type   string `json:"type"`
	X      int32  `json:"x,omitempty"`
	Y      int32  `json:"y,omitempty"`
	Button int    `json:"button,omitempty"`
	Down   bool   `json:"down,omitempty"`
	VK     uint16 `json:"vk,omitempty"`
	Text   string `json:"text,omitempty"`
}

func runSeamlessWindowBridge() (string, func(), error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", seamlessWindowPort))
	if err != nil {
		return "", nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		listener.Close()
		return "", nil, err
	}
	token := hex.EncodeToString(secret)
	// QEMU reads this file at startup. The value stays out of its command line
	// and the guest kernel command line. A protected ACL limits access to this
	// Windows user; the file is removed when the launcher exits.
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		listener.Close()
		return "", nil, err
	}
	tokenDir := filepath.Join(cacheDir, "Omarchy", "seamless")
	if err := os.MkdirAll(tokenDir, 0700); err != nil {
		listener.Close()
		return "", nil, err
	}
	file, err := os.CreateTemp(tokenDir, "token-*")
	if err != nil {
		listener.Close()
		return "", nil, err
	}
	path := file.Name()
	if _, err = file.WriteString(token); err == nil {
		err = file.Close()
	} else {
		file.Close()
	}
	if err != nil {
		listener.Close()
		os.Remove(path)
		return "", nil, err
	}
	if err := restrictSeamlessTokenWindows(path); err != nil {
		listener.Close()
		os.Remove(path)
		return "", nil, fmt.Errorf("protect bearer token: %w", err)
	}
	bridge := &seamlessWindowBridge{token: token, backend: nativeSeamlessWindows{}, frames: make(chan struct{}, 2)}
	if os.Getenv("OMARCHY_WINDOWS_PRESENTATION") == "native" {
		restore, dpiErr := nativeDPIEnter()
		if dpiErr != nil {
			listener.Close()
			os.Remove(path)
			return "", nil, dpiErr
		}
		restore()
		bridge.projection = newNativeProjection(bridge)
	}
	activeSeamlessBridge.Store(bridge)
	server := &http.Server{
		Handler:           bridge,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       4 * time.Second,
		WriteTimeout:      12 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	if bridge.projection == nil {
		prewarmSeamlessWGC()
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("seamless Windows bridge stopped: %v", err)
		}
	}()
	return path, func() {
		activeSeamlessBridge.CompareAndSwap(bridge, nil)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
		if bridge.projection != nil {
			bridge.projection.Close()
		} else {
			stopSeamlessWGC()
		}
		_ = os.Remove(path)
	}, nil
}

func (b *seamlessWindowBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(b.token)) != 1 {
		seamlessJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL.Path == "/v1/presentation" && r.Method == http.MethodGet {
		mode := "capture"
		if b.projection != nil {
			mode = "native"
		}
		seamlessJSON(w, http.StatusOK, map[string]any{"mode": mode, "protocol": 1})
		return
	}
	if r.URL.Path == "/v1/layout" && r.Method == http.MethodPost {
		if b.projection == nil {
			seamlessJSONError(w, http.StatusConflict, "native_presentation_disabled")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var layout nativeLayout
		if err := dec.Decode(&layout); err != nil || dec.Decode(new(any)) != io.EOF {
			seamlessJSONError(w, http.StatusBadRequest, "invalid_layout")
			return
		}
		if err := validateNativeLayout(layout); err != nil {
			seamlessJSONError(w, http.StatusBadRequest, "invalid_layout")
			return
		}
		suspended, err := b.projection.Apply(layout)
		if err != nil {
			seamlessJSONError(w, http.StatusConflict, "layout_unavailable")
			return
		}
		seamlessJSON(w, http.StatusOK, struct {
			Accepted  bool   `json:"accepted"`
			Suspended string `json:"suspended,omitempty"`
		}{Accepted: true, Suspended: suspended})
		return
	}
	if r.URL.Path == "/v1/windows" && r.Method == http.MethodGet {
		windows := b.catalogue()
		seamlessJSON(w, http.StatusOK, struct {
			Windows []seamlessWindow `json:"windows"`
		}{Windows: windows})
		return
	}
	path, ok := strings.CutPrefix(r.URL.Path, "/v1/windows/")
	if !ok {
		seamlessJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	id, action, ok := strings.Cut(path, "/")
	if !ok || id == "" || strings.Contains(action, "/") {
		seamlessJSONError(w, http.StatusNotFound, "not_found")
		return
	}
	var selected *seamlessWindow
	for _, window := range b.catalogue() {
		if window.ID == id {
			copy := window
			selected = &copy
			break
		}
	}
	if selected == nil {
		seamlessJSONError(w, http.StatusNotFound, "window_gone")
		return
	}
	switch {
	case action == "frame" && r.Method == http.MethodGet:
		if b.projection != nil {
			seamlessJSONError(w, http.StatusConflict, "native_presentation_active")
			return
		}
		select {
		case b.frames <- struct{}{}:
			defer func() { <-b.frames }()
		default:
			seamlessJSONError(w, http.StatusServiceUnavailable, "capture_busy")
			return
		}
		png, err := b.backend.frame(*selected)
		if err != nil {
			if errors.Is(err, errSeamlessGone) {
				seamlessJSONError(w, http.StatusNotFound, "window_gone")
				return
			}
			seamlessJSONError(w, http.StatusUnprocessableEntity, "capture_unavailable")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	case action == "input" && r.Method == http.MethodPost:
		if b.projection != nil {
			seamlessJSONError(w, http.StatusConflict, "native_presentation_active")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		var input seamlessInput
		if err := dec.Decode(&input); err != nil || !validSeamlessInput(input, *selected) {
			seamlessJSONError(w, http.StatusBadRequest, "invalid_input")
			return
		}
		if err := b.backend.input(*selected, input); err != nil {
			if errors.Is(err, errSeamlessGone) {
				seamlessJSONError(w, http.StatusNotFound, "window_gone")
				return
			}
			seamlessJSONError(w, http.StatusConflict, "input_unavailable")
			return
		}
		seamlessJSON(w, http.StatusOK, map[string]bool{"accepted": true})
	case action == "close" && r.Method == http.MethodPost:
		if err := b.backend.close(*selected); err != nil {
			if errors.Is(err, errSeamlessGone) {
				seamlessJSONError(w, http.StatusNotFound, "window_gone")
				return
			}
			seamlessJSONError(w, http.StatusConflict, "close_unavailable")
			return
		}
		seamlessJSON(w, http.StatusOK, map[string]bool{"accepted": true})
	case (action == "focus" || action == "resize") && r.Method == http.MethodPost:
		// Windows apps and QEMU share a host interactive session. Bringing a
		// host window forward can hide Omarchy; resizing can rearrange the
		// user's host desktop. GTK scales the captured frame instead.
		seamlessJSONError(w, http.StatusConflict, "shared_desktop_operation_unsupported")
	default:
		seamlessJSONError(w, http.StatusNotFound, "not_found")
	}
}

func (b *seamlessWindowBridge) catalogue() []seamlessWindow {
	if b.projection != nil {
		restore, err := nativeDPIEnter()
		if err != nil {
			return nil
		}
		defer restore()
	}
	items := b.backend.windows()
	windows := b.sharedWindows(items, time.Now())
	for i := range windows {
		windows[i].ID = b.windowID(windows[i])
	}
	return windows
}

func (b *seamlessWindowBridge) windowID(window seamlessWindow) string {
	identity := fmt.Sprintf("%x:%x", window.PID, window.handle)
	mac := hmac.New(sha256.New, []byte(b.token))
	_, _ = mac.Write([]byte(identity))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func seamlessKey(window seamlessWindow) seamlessWindowKey {
	return seamlessWindowKey{pid: window.PID, handle: window.handle}
}

// The host chooses grants. The bearer token only permits use of already
// granted windows; it cannot nominate a host HWND or inspect other titles.
func (b *seamlessWindowBridge) grantWindow(window seamlessWindow) bool {
	for _, current := range b.backend.windows() {
		if seamlessKey(current) == seamlessKey(window) && current.Class == window.Class {
			b.mu.Lock()
			if b.grants == nil {
				b.grants = make(map[seamlessWindowKey]string)
			}
			b.grants[seamlessKey(current)] = current.Class
			b.mu.Unlock()
			return true
		}
	}
	return false
}

func (b *seamlessWindowBridge) revokeWindow(window seamlessWindow) {
	b.mu.Lock()
	delete(b.grants, seamlessKey(window))
	b.mu.Unlock()
	if b.projection != nil {
		b.projection.Release(window)
	}
}

// Only a window created by the fresh process returned by ShellExecuteEx may
// be auto-granted. A single-instance app and an existing HWND need host choice.
func (b *seamlessWindowBridge) noteLaunch(pid uint32, before []seamlessWindow, now time.Time) bool {
	if pid == 0 {
		return false
	}
	previous := make(map[seamlessWindowKey]bool, len(before))
	for _, window := range before {
		if window.PID == pid {
			return false
		}
		previous[seamlessKey(window)] = true
	}
	b.mu.Lock()
	b.pending = append(b.pending, seamlessLaunch{pid: pid, until: now.Add(12 * time.Second), previous: previous})
	b.mu.Unlock()
	return true
}

func (b *seamlessWindowBridge) sharedWindows(windows []seamlessWindow, now time.Time) []seamlessWindow {
	// Tray reconciliation also calls sharedWindows directly. Include hidden
	// projected HWNDs there so changing Omarchy workspace cannot revoke a
	// still-owned grant just because EnumWindows omits hidden windows.
	if b.projection != nil {
		windows = append(windows, b.projection.HiddenWindows(windows)...)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	current := make(map[seamlessWindowKey]string, len(windows))
	for _, window := range windows {
		current[seamlessKey(window)] = window.Class
	}
	for key, class := range b.grants {
		if current[key] != class {
			delete(b.grants, key)
		}
	}
	pending := b.pending[:0]
	for _, launch := range b.pending {
		if now.After(launch.until) {
			logf("Windows app window was not found after launch; choose Show Windows app in Omarchy from the tray")
			continue
		}
		granted := false
		for _, window := range windows {
			key := seamlessKey(window)
			if window.PID != launch.pid || launch.previous[key] {
				continue
			}
			if b.grants == nil {
				b.grants = make(map[seamlessWindowKey]string)
			}
			b.grants[key] = window.Class
			granted = true
			break
		}
		if !granted {
			pending = append(pending, launch)
		}
	}
	b.pending = pending
	shared := make([]seamlessWindow, 0, len(b.grants))
	for _, window := range windows {
		if class, ok := b.grants[seamlessKey(window)]; ok && class == window.Class {
			shared = append(shared, window)
		}
	}
	return shared
}

func validSeamlessInput(input seamlessInput, window seamlessWindow) bool {
	switch input.Type {
	case "pointer":
		return input.X >= 0 && input.X < window.Width && input.Y >= 0 && input.Y < window.Height &&
			(input.Button >= 0 && input.Button <= 3)
	case "key":
		return input.VK > 0 && input.VK < 256
	case "text":
		return input.Text != "" && len(input.Text) <= 2048
	default:
		return false
	}
}

func seamlessJSON(w http.ResponseWriter, code int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(value)
}

func seamlessJSONError(w http.ResponseWriter, code int, errorCode string) {
	seamlessJSON(w, code, map[string]string{"error": errorCode})
}
