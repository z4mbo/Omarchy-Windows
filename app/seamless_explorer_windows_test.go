//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"
)

func TestExplorerCandidateRequiresExactNewMainWindow(t *testing.T) {
	base := seamlessWindow{PID: 400, Process: "explorer.exe", Class: "CabinetWClass", handle: 0x1234, created: 99, threadID: 8}
	other := base
	other.handle = 0x5678
	tests := []struct {
		name    string
		before  map[uintptr]bool
		visible []seamlessWindow
		hwnd    uintptr
		want    bool
	}{
		{"one exact new Explorer window", nil, []seamlessWindow{base}, base.handle, true},
		{"existing window", map[uintptr]bool{base.handle: true}, []seamlessWindow{base}, base.handle, false},
		{"COM returned another window", nil, []seamlessWindow{base}, other.handle, false},
		{"two concurrent Explorer windows", nil, []seamlessWindow{base, other}, base.handle, false},
		{"new tab reused old HWND", map[uintptr]bool{base.handle: true}, []seamlessWindow{base, other}, base.handle, false},
		{"missing visible window", nil, nil, base.handle, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := explorerCandidate(tt.before, tt.visible, tt.hwnd)
			if (err == nil) != tt.want {
				t.Fatalf("candidate=%+v err=%v, want accepted=%v", got, err, tt.want)
			}
		})
	}
}

func TestExplorerCandidateRejectsWeakIdentity(t *testing.T) {
	base := seamlessWindow{PID: 400, Process: "explorer.exe", Class: "CabinetWClass", handle: 0x1234, created: 99, threadID: 8}
	for _, change := range []func(*seamlessWindow){
		func(w *seamlessWindow) { w.Class = "WorkerW" },
		func(w *seamlessWindow) { w.Process = "notepad.exe" },
		func(w *seamlessWindow) { w.PID = 0 },
		func(w *seamlessWindow) { w.created = 0 },
		func(w *seamlessWindow) { w.threadID = 0 },
	} {
		window := base
		change(&window)
		if _, err := explorerCandidate(nil, []seamlessWindow{window}, base.handle); err == nil {
			t.Fatalf("accepted weak Explorer identity: %+v", window)
		}
	}
}

func TestExplorerCandidateWaitsOnlyForMissingVisibleWindow(t *testing.T) {
	window := seamlessWindow{PID: 400, Process: "explorer.exe", Class: "CabinetWClass", handle: 0x1234, created: 99, threadID: 8}
	if _, err := explorerCandidate(nil, nil, window.handle); !errors.Is(err, errExplorerPending) {
		t.Fatalf("new COM HWND before visible enumeration should stay pending: %v", err)
	}
	if got, err := explorerCandidate(nil, []seamlessWindow{window}, window.handle); err != nil || got.handle != window.handle {
		t.Fatalf("pending HWND did not become eligible: %+v, %v", got, err)
	}
	other := window
	other.handle++
	for _, visible := range [][]seamlessWindow{{other}, {window, other}} {
		if _, err := explorerCandidate(nil, visible, window.handle); err == nil || errors.Is(err, errExplorerPending) {
			t.Fatalf("ambiguous Explorer identity must fail closed, got %v", err)
		}
	}
}

func TestExplorerLaunchGateAllowsOnlyOneWorker(t *testing.T) {
	var gate explorerFlight
	if !gate.start() || gate.start() {
		t.Fatal("concurrent Explorer COM worker was accepted")
	}
	gate.done()
	if !gate.start() {
		t.Fatal("completed Explorer COM worker blocked the next request")
	}
	gate.done()
}

// This test creates a visible Explorer Home window. It is deliberately opt-in
// and is run only by the desktop test owner after coordinating UI state.
func TestExplorerCOMHomeGrantOptIn(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_EXPLORER_COM") != "1" {
		t.Skip("set OMARCHY_TEST_EXPLORER_COM=1 for the visible Explorer diagnostic")
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	bridge := &seamlessWindowBridge{token: hex.EncodeToString(secret[:]), backend: nativeSeamlessWindows{}}
	if !activeSeamlessBridge.CompareAndSwap(nil, bridge) {
		t.Fatal("another bridge is active in this test process")
	}
	defer func() {
		activeSeamlessBridge.CompareAndSwap(bridge, nil)
		bridge.closePendingLaunches() // prevents a late worker grant after a failed diagnostic
		bridge.closeGrants()          // releases only this test's grant marker; leaves Explorer open
	}()
	if err := shellOpenExplorerWindowApp(); err != nil {
		t.Fatalf("ShellBrowserWindow launch: %v", err)
	}
	until := time.Now().Add(explorerLaunchLifetime + time.Second)
	for time.Now().Before(until) {
		visible := bridge.backend.windows()
		bridge.mu.Lock()
		for _, window := range visible {
			grant, ok := bridge.grants[seamlessKey(window)]
			if !ok || !bridge.grantMatches(window, grant) {
				continue
			}
			bridge.mu.Unlock()
			t.Logf("Explorer COM Home grant: HWND=%#x PID=%d class=%q process=%q title=%q",
				window.handle, window.PID, window.Class, window.Process, window.Title)
			return
		}
		bridge.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("Explorer Home window opened but no exact new COM HWND was granted; leave the window for manual inspection")
}
