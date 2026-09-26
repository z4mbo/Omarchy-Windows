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
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const seamlessWindowPort = 4457

var errSeamlessCapture = errors.New("window capture unavailable")
var errSeamlessInput = errors.New("window input unavailable")
var errSeamlessGone = errors.New("window gone")

// This is a deliberately small, local-only prototype. Each Windows HWND is
// enumerated and captured separately so the guest can create one Wayland
// surface per application window. PrintWindow does not reliably capture GPU
// applications or exclusive fullscreen games; those need a WGC transport.
// Input is posted to an HWND without changing the host foreground window.
// Some applications, especially games, ignore posted messages.
type seamlessWindowBridge struct {
	token   string
	backend seamlessWindowBackend
	frames  chan struct{}
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
	server := &http.Server{
		Handler:           bridge,
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       4 * time.Second,
		WriteTimeout:      12 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logf("seamless Windows bridge stopped: %v", err)
		}
	}()
	return path, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
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
	windows := b.backend.windows()
	for i := range windows {
		identity := fmt.Sprintf("%x:%x", windows[i].PID, windows[i].handle)
		mac := hmac.New(sha256.New, []byte(b.token))
		_, _ = mac.Write([]byte(identity))
		windows[i].ID = hex.EncodeToString(mac.Sum(nil)[:16])
	}
	return windows
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
