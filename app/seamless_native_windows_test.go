//go:build windows

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestSeamlessAlignWGCFramePreservesWindowCoordinates(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 4, 3))
	source.Set(0, 0, color.RGBA{R: 255, A: 255})
	source.Set(3, 2, color.RGBA{B: 255, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, source); err != nil {
		t.Fatal(err)
	}
	outer := seamlessRect{left: 100, top: 200, right: 107, bottom: 205}
	visible := seamlessRect{left: 102, top: 201, right: 106, bottom: 204}
	output, err := seamlessAlignWGCFrame(encoded.Bytes(), outer, visible)
	if err != nil {
		t.Fatal(err)
	}
	aligned, err := png.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	if got := aligned.Bounds().Size(); got.X != 7 || got.Y != 5 {
		t.Fatalf("aligned size = %v, want 7x5", got)
	}
	if got := color.RGBAModel.Convert(aligned.At(2, 1)); got != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("visible top-left pixel = %v", got)
	}
	if got := color.RGBAModel.Convert(aligned.At(5, 3)); got != (color.RGBA{B: 255, A: 255}) {
		t.Fatalf("visible bottom-right pixel = %v", got)
	}
	if _, _, _, alpha := aligned.At(0, 0).RGBA(); alpha != 0 {
		t.Fatal("omitted resize border should be transparent")
	}
	centered, err := seamlessAlignWGCFrame(encoded.Bytes(), outer, seamlessRect{left: 101, top: 201, right: 106, bottom: 204})
	if err != nil {
		t.Fatalf("small composited rim should align: %v", err)
	}
	centeredImage, err := png.Decode(bytes.NewReader(centered))
	if err != nil || color.RGBAModel.Convert(centeredImage.At(1, 1)) != (color.RGBA{R: 255, A: 255}) {
		t.Fatalf("composited rim was not centered: %v", err)
	}
	if _, err := seamlessAlignWGCFrame(encoded.Bytes(), seamlessRect{right: 20, bottom: 20}, visible); err == nil {
		t.Fatal("large capture geometry mismatch must fail closed")
	}
	unchanged, err := seamlessAlignWGCFrame(encoded.Bytes(), seamlessRect{left: 0, top: 0, right: 4, bottom: 3}, visible)
	if err != nil || !bytes.Equal(unchanged, encoded.Bytes()) {
		t.Fatalf("full-window capture changed: %v", err)
	}
}

func TestSeamlessNativeInputTargetsChildWithoutHostFocus(t *testing.T) {
	if os.Getenv("TRYOMARCHY_NATIVE_UI_TEST") != "1" {
		t.Skip("requires an interactive Windows desktop")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	instance, _, _ := procGetModuleHandleW.Call(0)
	type windowClass struct {
		size, style                   uint32
		callback                      uintptr
		classExtra, windowExtra       int32
		instance, icon, cursor, brush uintptr
		menu, class                   *uint16
		smallIcon                     uintptr
	}
	rootClass, _ := syscall.UTF16PtrFromString("OmarchySeamlessInputTest")
	callback := syscall.NewCallback(func(hwnd, message, w, l uintptr) uintptr {
		result, _, _ := procDefWindowProcW.Call(hwnd, message, w, l)
		return result
	})
	wc := windowClass{size: uint32(unsafe.Sizeof(windowClass{})), callback: callback, instance: instance, class: rootClass}
	if registered, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); registered == 0 {
		t.Fatal(err)
	}
	defer user32.NewProc("UnregisterClassW").Call(uintptr(unsafe.Pointer(rootClass)), instance)
	staticClass, _ := syscall.UTF16PtrFromString("STATIC")
	editClass, _ := syscall.UTF16PtrFromString("EDIT")
	root, _, err := procCreateWindowExW.Call(0x08000000, uintptr(unsafe.Pointer(rootClass)), 0,
		wsPopup|wsVisible, 30000, 30000, 240, 160, 0, 0, instance, 0) // WS_EX_NOACTIVATE
	if root == 0 {
		t.Fatal(err)
	}
	defer procDestroyWindow.Call(root)
	panel, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(staticClass)), 0,
		wsChild|wsVisible, 10, 10, 120, 70, root, 1, instance, 0)
	if panel == 0 {
		t.Fatal(err)
	}
	edit, _, err := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(editClass)), 0,
		wsChild|wsVisible, 5, 5, 80, 25, panel, 2, instance, 0)
	if edit == 0 {
		t.Fatal(err)
	}
	other, _, err := procCreateWindowExW.Call(0x08000000, uintptr(unsafe.Pointer(staticClass)), 0,
		wsPopup|wsVisible, 30250, 30000, 100, 100, 0, 0, instance, 0)
	if other == 0 {
		t.Fatal(err)
	}
	defer procDestroyWindow.Call(other)

	var rootRect, editRect seamlessRect
	seamlessGetRect.Call(root, uintptr(unsafe.Pointer(&rootRect)))
	seamlessGetRect.Call(edit, uintptr(unsafe.Pointer(&editRect)))
	window := seamlessWindow{PID: uint32(os.Getpid()), handle: root, X: rootRect.left, Y: rootRect.top,
		Width: rootRect.right - rootRect.left, Height: rootRect.bottom - rootRect.top}
	click := seamlessPoint{editRect.left + 3, editRect.top + 3}
	if !seamlessValidInputTarget(window, edit) || seamlessValidInputTarget(window, other) {
		t.Fatal("child containment or process check failed")
	}
	if target := seamlessPointerTarget(window, click); target != edit {
		t.Fatalf("nested hit test chose %#x, want edit %#x (root %#x, panel %#x, root rect %+v, edit rect %+v)", target, edit, root, panel, rootRect, editRect)
	}
	if target := seamlessPointerTarget(window, seamlessPoint{rootRect.right - 10, rootRect.bottom - 10}); target != root {
		t.Fatalf("empty area chose %#x, want root %#x", target, root)
	}

	foreground, _, _ := procGetForegroundWindow.Call()
	if err := (nativeSeamlessWindows{}).input(window, seamlessInput{Type: "pointer", X: click.x - rootRect.left, Y: click.y - rootRect.top, Button: 1, Down: true}); err != nil {
		t.Fatal(err)
	}
	if target := seamlessKeyboardTarget(window); target != edit {
		t.Fatalf("keyboard chose %#x after click, want edit %#x", target, edit)
	}
	if err := (nativeSeamlessWindows{}).input(window, seamlessInput{Type: "pointer", X: 180, Y: 120, Button: 1}); err != nil {
		t.Fatal(err)
	}
	if err := (nativeSeamlessWindows{}).input(window, seamlessInput{Type: "text", Text: "Child"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		var message msgStruct
		if pending, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0, pmRemove); pending == 0 {
			break
		}
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
	var value [32]uint16
	procGetWindowTextW.Call(edit, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)))
	if got := syscall.UTF16ToString(value[:]); got != "Child" {
		t.Fatalf("edit received %q, want Child", got)
	}
	if now, _, _ := procGetForegroundWindow.Call(); now != foreground {
		t.Fatalf("input changed host foreground from %#x to %#x", foreground, now)
	}
	procDestroyWindow.Call(edit)
	if selected := seamlessSelectedTarget(window); selected.target != 0 {
		t.Fatalf("destroyed child remained selected: %#x", selected.target)
	}
}

// Opt-in physical diagnostic for the custom Character Map grid. The window
// must already be open on the interactive desktop; this test never launches
// an app or changes the host foreground window.
func TestSeamlessPhysicalCharacterMapGridClick(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_CHARMAP_INPUT") != "1" {
		t.Skip("set OMARCHY_TEST_CHARMAP_INPUT=1 for a local Character Map input check")
	}
	var matches []seamlessWindow
	for _, window := range (nativeSeamlessWindows{}).windows() {
		if strings.EqualFold(window.Process, "charmap.exe") && window.Title == "Character Map" {
			matches = append(matches, window)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one open Character Map window; found %d", len(matches))
	}
	window := matches[0]
	grid, _, _ := user32.NewProc("GetDlgItem").Call(window.handle, 108)
	if !seamlessValidInputTarget(window, grid) {
		t.Fatal("Character Map grid is not a valid input target")
	}
	var rect seamlessRect
	if ok, _, _ := seamlessGetRect.Call(grid, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		t.Fatal("cannot locate Character Map grid")
	}
	// Arial starts with U+0021; B is 33 cells later, on row 1, column 13.
	x := rect.left + (rect.right-rect.left)*27/40
	y := rect.top + (rect.bottom-rect.top)*3/20
	if target := seamlessPointerTarget(window, seamlessPoint{x, y}); target != grid {
		t.Fatalf("B cell hit test selected %#x instead of grid %#x", target, grid)
	}
	input := seamlessInput{Type: "pointer", X: x - window.X, Y: y - window.Y, Button: 1, Down: true}
	if err := (nativeSeamlessWindows{}).input(window, input); err != nil {
		t.Fatal(err)
	}
	input.Down = false
	if err := (nativeSeamlessWindows{}).input(window, input); err != nil {
		t.Fatal(err)
	}
	selected, _, _ := user32.NewProc("GetDlgItem").Call(window.handle, 501)
	if selected == 0 {
		t.Fatal("Character Map selected-character label is unavailable")
	}
	var value [128]uint16
	procGetWindowTextW.Call(selected, uintptr(unsafe.Pointer(&value[0])), uintptr(len(value)))
	if got := syscall.UTF16ToString(value[:]); !strings.Contains(got, "U+0042") {
		t.Fatalf("posted click selected %q instead of U+0042", got)
	}
	t.Logf("posted B click to Character Map grid at window-local (%d,%d), grid %#x", input.X, input.Y, grid)
}

// Disposable second bridge for a guest UI check against this source tree. It
// shares only an already open Character Map window and never touches the
// installed launcher, production bridge port, or guest disk image.
func TestSeamlessPhysicalIsolatedBridge(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_ISOLATED_BRIDGE") != "1" {
		t.Skip("set OMARCHY_TEST_ISOLATED_BRIDGE=1 for a 90-second guest UI check")
	}
	var matches []seamlessWindow
	for _, window := range (nativeSeamlessWindows{}).windows() {
		if strings.EqualFold(window.Process, "charmap.exe") && window.Title == "Character Map" {
			matches = append(matches, window)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one open Character Map window; found %d", len(matches))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:4458")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	// This temporary token has no authority on the normal port 4457 bridge.
	bridge := &seamlessWindowBridge{token: strings.Repeat("a", 64), backend: nativeSeamlessWindows{}, frames: make(chan struct{}, 2)}
	if !bridge.grantWindow(matches[0]) {
		t.Fatal("Character Map grant failed")
	}
	server := &http.Server{Handler: bridge, ReadHeaderTimeout: 2 * time.Second, WriteTimeout: 12 * time.Second}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	t.Setenv(seamlessWGCEnv, "1")
	prewarmSeamlessWGC()
	defer stopSeamlessWGC()
	t.Logf("isolated bridge on 127.0.0.1:4458, Character Map ID %s; expires in 90 seconds", bridge.windowID(matches[0]))
	time.Sleep(90 * time.Second)
}
