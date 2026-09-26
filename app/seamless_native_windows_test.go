//go:build windows

package main

import (
	"os"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

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
