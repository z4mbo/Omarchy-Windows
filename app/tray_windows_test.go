//go:build windows

package main

import (
	"strings"
	"testing"
	"time"
)

func TestTrayChoicesKeepHiddenGrantRevocableWithoutOfferingItAgain(t *testing.T) {
	visible := seamlessWindow{PID: 100, handle: 0x100, Class: "Editor", Title: "Visible"}
	hidden := seamlessWindow{PID: 200, handle: 0x200, Class: "Game", Title: "Hidden"}
	closed := seamlessWindow{PID: 300, handle: 0x300, Class: "Closed", Title: "Closed"}
	backend := &fakeSeamlessBackend{items: []seamlessWindow{visible}}
	bridge := &seamlessWindowBridge{
		token: strings.Repeat("a", 64), backend: backend,
		grants: map[seamlessWindowKey]string{
			seamlessKey(hidden): hidden.Class,
			seamlessKey(closed): closed.Class,
		},
	}
	hiddenOwned := true
	bridge.projection = &nativeProjection{bridge: bridge,
		hiddenWindowSourceForTest: func([]seamlessWindow) []seamlessWindow {
			if hiddenOwned {
				return []seamlessWindow{hidden}
			}
			return nil
		},
	}

	windows := backend.windows()
	share, stop := traySeamlessWindowChoices(windows, bridge.sharedWindows(windows, time.Now()))
	if len(share) != 1 || seamlessKey(share[0]) != seamlessKey(visible) {
		t.Fatalf("new grants should only offer the visible unshared window: %+v", share)
	}
	if len(stop) != 1 || seamlessKey(stop[0]) != seamlessKey(hidden) {
		t.Fatalf("only the verified hidden grant should be revocable: %+v", stop)
	}
	if _, ok := bridge.grants[seamlessKey(closed)]; ok {
		t.Fatal("closed window grant survived reconciliation")
	}

	hiddenOwned = false // Its HWND is no longer valid or owned by the projector.
	share, stop = traySeamlessWindowChoices(windows, bridge.sharedWindows(windows, time.Now()))
	if len(share) != 1 || len(stop) != 0 {
		t.Fatalf("stale hidden window remained in tray choices: share=%+v stop=%+v", share, stop)
	}
}
