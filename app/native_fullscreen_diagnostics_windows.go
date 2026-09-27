//go:build windows

package main

import (
	"fmt"
	"os"
	"sync"
	"time"
	"unsafe"
)

// This is deliberately off by default. It records only Win32 presentation
// measurements for an already granted HWND, never titles or grant properties.
var nativeFullscreenDiagnosticsEnabled = os.Getenv("OMARCHY_NATIVE_FULLSCREEN_DIAGNOSTICS") == "1"

const (
	nativeFullscreenDiagnosticTTL = 2 * time.Minute
	nativeFullscreenDiagnosticCap = 32
)

type nativeFullscreenDiagnosticKey struct {
	pid         uint32
	hwnd        uintptr
	created     uint64
	incarnation uintptr
}

func nativeFullscreenDiagnosticIdentity(state *nativeProjectedWindow) nativeFullscreenDiagnosticKey {
	return nativeFullscreenDiagnosticKey{pid: state.window.PID, hwnd: state.window.handle,
		created: state.created, incarnation: state.window.incarnation}
}

type nativeFullscreenDiagnosticRecord struct {
	signature string
	logged    time.Time
	seen      time.Time
}

// A projected-window snapshot can be recreated on every rejected layout.
// Keep the rate gate on the bridge projection, keyed to the verified grant
// identity, so those replacements cannot reset it.
type nativeFullscreenDiagnosticLimiter struct {
	mu      sync.Mutex
	records map[nativeFullscreenDiagnosticKey]nativeFullscreenDiagnosticRecord
}

func (l *nativeFullscreenDiagnosticLimiter) allow(key nativeFullscreenDiagnosticKey, signature string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.records == nil {
		l.records = make(map[nativeFullscreenDiagnosticKey]nativeFullscreenDiagnosticRecord)
	}
	for candidate, record := range l.records {
		if now.Sub(record.seen) >= nativeFullscreenDiagnosticTTL {
			delete(l.records, candidate)
		}
	}
	if record, ok := l.records[key]; ok {
		due := nativeFullscreenDiagnosticDue(record.signature, signature, record.logged, now)
		record.seen = now
		if due {
			record.signature, record.logged = signature, now
		}
		l.records[key] = record
		return due
	}
	if len(l.records) >= nativeFullscreenDiagnosticCap {
		var oldestKey nativeFullscreenDiagnosticKey
		var oldest time.Time
		for candidate, record := range l.records {
			if oldest.IsZero() || record.seen.Before(oldest) {
				oldestKey, oldest = candidate, record.seen
			}
		}
		delete(l.records, oldestKey)
	}
	l.records[key] = nativeFullscreenDiagnosticRecord{signature: signature, logged: now, seen: now}
	return true
}

func nativeFullscreenDiagnosticDue(previous, current string, last, now time.Time) bool {
	if now.Sub(last) < 2*time.Second {
		return false
	}
	return previous != current || now.Sub(last) >= 30*time.Second
}

func nativeLogFullscreenDiagnostic(state *nativeProjectedWindow, outer seamlessRect,
	previous, presentation nativeWindowPresentation, shown bool, reason string) {
	if !nativeFullscreenDiagnosticsEnabled || state == nil || state.fullscreenDiagnostics == nil {
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
	key := nativeFullscreenDiagnosticIdentity(state)
	if !state.fullscreenDiagnostics.allow(key, signature, now) {
		return
	}
	logf("native fullscreen diagnostic: %s", signature)
}
