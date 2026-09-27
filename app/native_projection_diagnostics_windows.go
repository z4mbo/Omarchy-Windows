//go:build windows

package main

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

const nativeZOrderDiagnosticInterval = 30 * time.Second

var (
	nativeIsIconic         = user32.NewProc("IsIconic")
	nativeZOrderDiagnostic = struct {
		sync.Mutex
		key string
		at  time.Time
	}{}
)

type nativeZOrderWindowFacts struct {
	hwnd    uintptr
	pid     uint32
	style   uintptr
	valid   bool
	visible bool
	iconic  bool
}

func nativeReadZOrderWindowFacts(hwnd uintptr) nativeZOrderWindowFacts {
	facts := nativeZOrderWindowFacts{hwnd: hwnd}
	if hwnd == 0 {
		return facts
	}
	valid, _, _ := seamlessIsWindow.Call(hwnd)
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&facts.pid)))
	facts.style, _, _ = nativeGetWindowLongPtr.Call(hwnd, ^uintptr(19)) // GWL_EXSTYLE = -20
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	iconic, _, _ := nativeIsIconic.Call(hwnd)
	facts.valid, facts.visible, facts.iconic = valid != 0, visible != 0, iconic != 0
	return facts
}

func nativeShouldLogZOrderDiagnostic(previousKey string, previousAt time.Time, key string, now time.Time) bool {
	return key != previousKey || previousAt.IsZero() || now.Sub(previousAt) >= nativeZOrderDiagnosticInterval
}

// Called only when z-order placement fails. No titles or command lines are read.
func nativeLogZOrderFailure(reason string, hwnd uintptr) {
	qemu := nativeReadZOrderWindowFacts(qemuHwnd.Load())
	projected := nativeReadZOrderWindowFacts(hwnd)
	foregroundHWND, _, _ := nativeGetForegroundWindow.Call()
	var foregroundPID uint32
	if foregroundHWND != 0 {
		procGetWindowThreadProcessId.Call(foregroundHWND, uintptr(unsafe.Pointer(&foregroundPID)))
	}
	const topmost = uintptr(0x8) // WS_EX_TOPMOST
	key := fmt.Sprintf("%s/%x/%d/%x/%d/%t/%t/%t/%t/%t/%t/%t/%t", reason,
		qemu.hwnd, qemu.pid, projected.hwnd, projected.pid,
		qemu.style&topmost != 0, projected.style&topmost != 0,
		qemu.valid, projected.valid, qemu.visible, projected.visible, qemu.iconic, projected.iconic)
	now := time.Now()
	nativeZOrderDiagnostic.Lock()
	if !nativeShouldLogZOrderDiagnostic(nativeZOrderDiagnostic.key, nativeZOrderDiagnostic.at, key, now) {
		nativeZOrderDiagnostic.Unlock()
		return
	}
	nativeZOrderDiagnostic.key, nativeZOrderDiagnostic.at = key, now
	nativeZOrderDiagnostic.Unlock()
	logf("native projection z order diagnostic: reason=%s qemu_hwnd=0x%x qemu_pid=%d qemu_valid=%t qemu_exstyle=0x%x qemu_topmost=%t qemu_visible=%t qemu_iconic=%t native_hwnd=0x%x native_pid=%d native_valid=%t native_exstyle=0x%x native_topmost=%t native_visible=%t native_iconic=%t foreground_hwnd=0x%x foreground_pid=%d",
		reason, qemu.hwnd, qemu.pid, qemu.valid, qemu.style, qemu.style&topmost != 0, qemu.visible, qemu.iconic,
		projected.hwnd, projected.pid, projected.valid, projected.style, projected.style&topmost != 0, projected.visible, projected.iconic,
		foregroundHWND, foregroundPID)
}
