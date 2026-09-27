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
	"syscall"
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
	token        string
	tokenPath    string
	session      atomic.Pointer[seamlessSession]
	backend      seamlessWindowBackend
	frames       chan struct{}
	projection   *nativeProjection
	nativeModeMu sync.RWMutex // excludes in-flight capture/input while native projection activates
	nativeActive bool         // sticky for this bridge boot after the first valid layout
	captureWarm  sync.Once
	mu           sync.Mutex
	grants       map[seamlessWindowKey]seamlessGrant
	retired      []seamlessRetiredGrant
	pending      []seamlessLaunch
	closed       bool
}

// Grant identities use the bridge's stable token as an HMAC key. The bearer
// presented by a guest is separate and changes for each QEMU process.
type seamlessSession struct {
	token      string
	generation uint64
}

type seamlessWindowKey struct {
	pid    uint32
	handle uintptr
}

type seamlessGrant struct {
	class       string
	created     uint64
	threadID    uint32
	incarnation uintptr
}

type seamlessRetiredGrant struct {
	key   seamlessWindowKey
	grant seamlessGrant
}

type seamlessLaunch struct {
	pid          uint32
	created      uint64         // FILETIME of the ShellExecuteEx process; zero only in legacy tests
	handle       syscall.Handle // retained until grant, expiry, or bridge shutdown
	until        time.Time
	previous     map[seamlessWindowKey]bool
	lastError    string
	selectorName string // nonempty only for an explicit guest app request
}

type seamlessWindowBackend interface {
	windows() []seamlessWindow
	frame(seamlessWindow) ([]byte, error)
	input(seamlessWindow, seamlessInput) error
	close(seamlessWindow) error
}

type seamlessWindow struct {
	ID            string `json:"id"`
	HWND          string `json:"hwnd"`
	PID           uint32 `json:"pid"`
	Title         string `json:"title"`
	Class         string `json:"class"`
	Process       string `json:"process"`
	AppGroup      string `json:"appGroup"`
	X             int32  `json:"x"`
	Y             int32  `json:"y"`
	Width         int32  `json:"width"`
	Height        int32  `json:"height"`
	Fullscreen    bool   `json:"fullscreen"`
	handle        uintptr
	created       uint64  // process creation FILETIME, captured during enumeration
	threadID      uint32  // GUI thread that owned this HWND during enumeration
	incarnation   uintptr // property marker on a granted native HWND
	grantProperty string  // bridge-specific Win32 property name
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

func runSeamlessWindowBridge(experimentalNativeForeground bool) (string, func(), error) {
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
	bridge := &seamlessWindowBridge{token: token, tokenPath: path, backend: nativeSeamlessWindows{}, frames: make(chan struct{}, 2)}
	bridge.session.Store(&seamlessSession{token: token})
	if os.Getenv("OMARCHY_WINDOWS_PRESENTATION") == "native" {
		restore, dpiErr := nativeDPIEnter()
		if dpiErr != nil {
			listener.Close()
			os.Remove(path)
			return "", nil, dpiErr
		}
		restore()
		bridge.projection = newNativeProjection(bridge)
		bridge.projection.experimentalForeground = experimentalNativeForeground
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
		bridge.closePendingLaunches()
		if bridge.projection != nil {
			bridge.projection.Close()
		}
		bridge.closeGrants()
		// A legacy guest may have used capture before native activation.
		stopSeamlessWGC()
		_ = os.Remove(path)
	}, nil
}

// QEMU reads the existing protected fw_cfg file only when its process starts.
// A fresh bearer rejects queued requests from the previous process while the
// bridge's stable HMAC key keeps explicit host window grants intact.
func prepareSeamlessSessionToken(path string, generation uint64) error {
	if path == "" {
		return nil // The optional bridge did not start.
	}
	b := activeSeamlessBridge.Load()
	if b == nil || b.tokenPath != path || generation == 0 || qemuPid.Load() != 0 {
		return errors.New("seamless bridge is not ready for a new QEMU process")
	}
	previous := b.session.Load()
	if previous == nil || generation <= previous.generation {
		return errors.New("seamless session generation did not advance")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("seamless token path is not a regular file")
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, token)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	if closeErr != nil {
		return closeErr
	}
	if err := restrictSeamlessTokenWindows(path); err != nil {
		return fmt.Errorf("protect renewed seamless token: %w", err)
	}
	b.session.Store(&seamlessSession{token: token, generation: generation})
	return nil
}

func (b *seamlessWindowBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	session := b.session.Load()
	if session == nil { // Existing in-process tests construct a bridge directly.
		session = &seamlessSession{token: b.token}
	}
	provided, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || subtle.ConstantTimeCompare([]byte(provided), []byte(session.token)) != 1 {
		seamlessJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if r.URL.Path == "/v1/presentation" && r.Method == http.MethodGet {
		mode := "capture"
		if b.projection != nil {
			mode = "native"
			seamlessJSON(w, http.StatusOK, map[string]any{"mode": mode, "protocol": 1,
				"capabilities": map[string]any{"occlusions": map[string]any{
					"maxRectsPerWindow": nativeMaxOcclusions, "coordinates": "tile"}}})
			return
		}
		seamlessJSON(w, http.StatusOK, map[string]any{"mode": mode, "protocol": 1})
		return
	}
	if r.URL.Path == "/v1/layout" && r.Method == http.MethodPost {
		if b.projection == nil {
			seamlessJSONError(w, http.StatusConflict, "native_presentation_disabled")
			return
		}
		// The token and generation were captured together before parsing. A
		// request from a previous QEMU process cannot become current later.
		requestGeneration := session.generation
		// Eight tiles with up to sixteen optional occlusion rectangles each.
		r.Body = http.MaxBytesReader(w, r.Body, 16384)
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
		b.nativeModeMu.Lock()
		// Older guests never submit layouts and retain the capture path even
		// when this host was configured for native projection. From the first
		// valid native layout onward, do not mix synthetic input with HWNDs.
		b.nativeActive = true
		suspended, err := b.projection.Apply(layout, requestGeneration)
		b.nativeModeMu.Unlock()
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
		b.nativeModeMu.RLock()
		defer b.nativeModeMu.RUnlock()
		if b.nativeActive {
			seamlessJSONError(w, http.StatusConflict, "native_presentation_active")
			return
		}
		if b.projection != nil {
			if _, native := b.backend.(nativeSeamlessWindows); native {
				b.captureWarm.Do(prewarmSeamlessWGC)
			}
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
		b.nativeModeMu.RLock()
		defer b.nativeModeMu.RUnlock()
		if b.nativeActive {
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
		if current := b.session.Load(); current != nil && current != session {
			seamlessJSONError(w, http.StatusUnauthorized, "previous_guest_session")
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
		if current := b.session.Load(); current != nil && current != session {
			seamlessJSONError(w, http.StatusUnauthorized, "previous_guest_session")
			return
		}
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
	if b.projection != nil {
		b.projection.StabilizeFullscreen(windows)
	}
	for i := range windows {
		windows[i].ID = b.windowID(windows[i])
	}
	return windows
}

func (b *seamlessWindowBridge) windowID(window seamlessWindow) string {
	identity := fmt.Sprintf("%x:%x:%x:%x", window.PID, window.handle, window.created, window.incarnation)
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
	return b.grantWindowChecked(window) == nil
}

func (b *seamlessWindowBridge) grantWindowChecked(window seamlessWindow) error {
	for _, current := range b.backend.windows() {
		selector := window.grantProperty == b.selectorPropertyName() && window.incarnation != 0
		if seamlessKey(current) == seamlessKey(window) && current.Class == window.Class &&
			current.created == window.created && current.threadID == window.threadID &&
			(current.incarnation == window.incarnation || (selector && current.incarnation == 0)) {
			if selector {
				current.incarnation = window.incarnation
				current.grantProperty = window.grantProperty
				if !seamlessWindowStillMatches(current) {
					return errors.New("window identity changed before selection")
				}
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if b.closed {
				return errors.New("Windows app bridge has stopped")
			}
			if existing, ok := b.grants[seamlessKey(current)]; ok && b.grantMatches(current, existing) {
				return nil
			}
			grant, err := b.newGrant(current)
			if err != nil {
				return err
			}
			if selector && nativeWindowProperty(current.handle, window.grantProperty) != window.incarnation {
				nativeRemoveGrantProperty(current.handle, b.grantPropertyName(), grant.incarnation)
				return errors.New("window identity changed during selection")
			}
			if b.grants == nil {
				b.grants = make(map[seamlessWindowKey]seamlessGrant)
			}
			b.grants[seamlessKey(current)] = grant
			return nil
		}
	}
	return errors.New("window identity changed before grant")
}

func (b *seamlessWindowBridge) revokeWindow(window seamlessWindow) {
	b.mu.Lock()
	key := seamlessKey(window)
	grant, granted := b.grants[key]
	if !granted || !b.grantMatches(window, grant) {
		b.mu.Unlock()
		return
	}
	delete(b.grants, key)
	b.mu.Unlock()
	if b.projection != nil {
		b.projection.Release(window)
	}
	if granted {
		b.retireGrant(key, grant)
	}
}

// A single-instance app and an existing HWND need host choice. The legacy
// wrapper keeps direct-PID tests independent of host process enumeration.
func (b *seamlessWindowBridge) noteLaunch(pid uint32, before []seamlessWindow, now time.Time) bool {
	return b.noteLaunchProcess(pid, 0, 0, before, now)
}

// The caller transfers ownership of handle only when this returns true.
func (b *seamlessWindowBridge) noteLaunchProcess(pid uint32, created uint64, handle syscall.Handle, before []seamlessWindow, now time.Time) bool {
	return b.noteLaunchProcessNamed(pid, created, handle, before, "", now)
}

func (b *seamlessWindowBridge) noteLaunchProcessNamed(pid uint32, created uint64, handle syscall.Handle, before []seamlessWindow, name string, now time.Time) bool {
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
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	b.pending = append(b.pending, seamlessLaunch{pid: pid, created: created, handle: handle, until: now.Add(12 * time.Second), previous: previous, selectorName: name})
	return true
}

func (b *seamlessWindowBridge) closePendingLaunches() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, launch := range b.pending {
		if launch.handle != 0 {
			_ = syscall.CloseHandle(launch.handle)
		}
	}
	b.pending = nil
}

func (b *seamlessWindowBridge) sharedWindows(windows []seamlessWindow, now time.Time) []seamlessWindow {
	// Tray reconciliation also calls sharedWindows directly. Include hidden
	// projected HWNDs there so changing Omarchy workspace cannot revoke a
	// still-owned grant just because EnumWindows omits hidden windows.
	if b.projection != nil {
		windows = append(windows, b.projection.HiddenWindows(windows)...)
	}
	b.mu.Lock()
	defer func() {
		b.mu.Unlock()
		b.cleanupRetiredGrants()
	}()
	current := make(map[seamlessWindowKey]seamlessWindow, len(windows))
	for _, window := range windows {
		current[seamlessKey(window)] = window
	}
	for key, grant := range b.grants {
		window, exists := current[key]
		if !exists || !b.grantMatches(window, grant) {
			delete(b.grants, key)
			b.retired = append(b.retired, seamlessRetiredGrant{key: key, grant: grant})
		}
	}
	var parents map[uint32]uint32
	for _, launch := range b.pending {
		if launch.created != 0 && launch.handle != 0 {
			parents, _ = seamlessProcessParents() // failure leaves descendants ungranted
			break
		}
	}
	pending := b.pending[:0]
	for _, launch := range b.pending {
		if now.After(launch.until) {
			if launch.handle != 0 {
				_ = syscall.CloseHandle(launch.handle)
			}
			if launch.lastError != "" {
				logf("Windows app window could not be granted: %s", launch.lastError)
			}
			if launch.selectorName != "" {
				if b.offerWindowSelectorFromKeys(launch.selectorName, launch.previous, now) {
					logf("Windows app window was not found after launch; offered the explicit Windows window selector")
				} else {
					logf("Windows app window was not found after launch; use Show Windows app in Omarchy from the tray")
				}
			} else if launch.lastError == "" {
				logf("Windows app window was not found after launch; choose Show Windows app in Omarchy from the tray")
			}
			continue
		}
		granted := false
		for _, window := range windows {
			key := seamlessKey(window)
			if launch.previous[key] {
				continue
			}
			if launch.created == 0 {
				if window.PID != launch.pid {
					continue
				}
			} else {
				created, err := nativeProcessCreated(window.PID)
				if err != nil {
					continue
				}
				var rootExit uint64
				if window.PID != launch.pid {
					var valid bool
					rootExit, valid = seamlessProcessExit(launch.handle)
					if !valid {
						continue
					}
				}
				if !launchedProcessEligible(launch, window.PID, parents[window.PID], created, rootExit) {
					continue
				}
				if !seamlessWindowStillMatches(window) {
					continue
				}
			}
			grant, err := b.newGrant(window)
			if err != nil {
				launch.lastError = err.Error()
				continue
			}
			if b.grants == nil {
				b.grants = make(map[seamlessWindowKey]seamlessGrant)
			}
			b.grants[key] = grant
			granted = true
			break
		}
		if !granted {
			pending = append(pending, launch)
		} else if launch.handle != 0 {
			_ = syscall.CloseHandle(launch.handle)
		}
	}
	b.pending = pending
	shared := make([]seamlessWindow, 0, len(b.grants))
	for _, window := range windows {
		if grant, ok := b.grants[seamlessKey(window)]; ok && b.grantMatches(window, grant) {
			window.incarnation = grant.incarnation
			window.grantProperty = b.grantPropertyName()
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
