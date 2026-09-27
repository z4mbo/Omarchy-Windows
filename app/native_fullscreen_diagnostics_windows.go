//go:build windows

package main

import (
	"fmt"
	"os"
	"time"
	"unsafe"
)

// This is deliberately off by default. It records only Win32 presentation
// measurements for an already granted HWND, never titles or grant properties.
var nativeFullscreenDiagnosticsEnabled = os.Getenv("OMARCHY_NATIVE_FULLSCREEN_DIAGNOSTICS") == "1"

func nativeFullscreenDiagnosticDue(previous, current string, last, now time.Time) bool {
	if now.Sub(last) < 2*time.Second {
		return false
	}
	return previous != current || now.Sub(last) >= 30*time.Second
}

func nativeLogFullscreenDiagnostic(state *nativeProjectedWindow, outer seamlessRect,
	previous, presentation nativeWindowPresentation, shown bool, reason string) {
	if !nativeFullscreenDiagnosticsEnabled || state == nil {
		return
	}
	hwnd := state.window.handle
	var dwm seamlessRect
	hr, _, _ := seamlessDwmAttr.Call(hwnd, 9, uintptr(unsafe.Pointer(&dwm)), unsafe.Sizeof(dwm))
	dwmKnown := hr == 0 && dwm.right > dwm.left && dwm.bottom > dwm.top
	var monitor, work seamlessRect
	monitorKnown := false
	if handle, _, _ := seamlessMonitor.Call(hwnd, 2); handle != 0 {
		info := seamlessMonitorInfoStruct{size: uint32(unsafe.Sizeof(seamlessMonitorInfoStruct{}))}
		if ok, _, _ := seamlessMonitorInfo.Call(handle, uintptr(unsafe.Pointer(&info))); ok != 0 {
			monitor, work, monitorKnown = info.monitor, info.work, true
		}
	}
	exStyle, _, _ := nativeGetWindowLongPtr.Call(hwnd, ^uintptr(19)) // GWL_EXSTYLE
	signature := fmt.Sprintf("pid=%d hwnd=%x reason=%s shown=%t outer=%v dwm=%v dwmKnown=%t monitor=%v work=%v monitorKnown=%t style=%x exStyle=%x showCmd=%d prevStyle=%x prevShowCmd=%d intent=%t",
		state.window.PID, hwnd, reason, shown, outer, dwm, dwmKnown, monitor, work, monitorKnown,
		presentation.style, exStyle, presentation.showCmd, previous.style, previous.showCmd, state.fullscreenIntent)
	now := time.Now()
	// One changed measurement at most every two seconds, with a periodic
	// sample no more often than every thirty seconds if the state is steady.
	if !nativeFullscreenDiagnosticDue(state.lastFullscreenDiag, signature, state.lastFullscreenDiagAt, now) {
		return
	}
	state.lastFullscreenDiag, state.lastFullscreenDiagAt = signature, now
	logf("native fullscreen diagnostic: %s", signature)
}
