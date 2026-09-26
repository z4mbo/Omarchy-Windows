//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func nativeTestLayout() nativeLayout {
	var l nativeLayout
	l.Sequence = 1
	l.Output.Width, l.Output.Height = 2560, 1440
	l.Windows = []nativeTile{{ID: strings.Repeat("a", 32), X: 100, Y: 200, Width: 400, Height: 300, Visible: true},
		{ID: strings.Repeat("b", 32), X: 0, Y: 0, Width: 1, Height: 1, Visible: false}}
	return l
}

func TestTrayReconciliationRetainsHiddenNativeGrant(t *testing.T) {
	w := seamlessWindow{PID: 4711, handle: 0xabc, Class: "OwnedEditor", Title: "Editor"}
	backend := &fakeSeamlessBackend{items: []seamlessWindow{w}}
	b := &seamlessWindowBridge{token: strings.Repeat("d", 64), backend: backend,
		grants: map[seamlessWindowKey]string{seamlessKey(w): w.Class}}
	p := &nativeProjection{bridge: b, hiddenWindowSourceForTest: func(visible []seamlessWindow) []seamlessWindow {
		if len(visible) == 0 {
			return []seamlessWindow{w}
		}
		return nil
	}}
	b.projection = p
	backend.items = nil // SW_HIDE omitted it from native EnumWindows.
	if got := b.sharedWindows(backend.windows(), time.Now()); len(got) != 1 || seamlessKey(got[0]) != seamlessKey(w) {
		t.Fatalf("tray reconciliation revoked hidden native window: %+v", got)
	}
	backend.items = []seamlessWindow{w}
	if got := b.sharedWindows(backend.windows(), time.Now()); len(got) != 1 {
		t.Fatalf("grant did not survive workspace return: %+v", got)
	}
}

func TestNativeLayoutRejectsUnboundedOrAmbiguousRects(t *testing.T) {
	if err := validateNativeLayout(nativeTestLayout()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(*nativeLayout)
	}{
		{"zero sequence", func(l *nativeLayout) { l.Sequence = 0 }},
		{"negative x", func(l *nativeLayout) { l.Windows[0].X = -1 }},
		{"edge overflow", func(l *nativeLayout) { l.Windows[0].X = 2500; l.Windows[0].Width = 100 }},
		{"hidden overflow", func(l *nativeLayout) { l.Windows[1].Height = 1441 }},
		{"zero hidden size", func(l *nativeLayout) { l.Windows[1].Width = 0 }},
		{"duplicate ID", func(l *nativeLayout) { l.Windows[1].ID = l.Windows[0].ID }},
		{"uppercase ID", func(l *nativeLayout) { l.Windows[0].ID = strings.Repeat("A", 32) }},
		{"large output", func(l *nativeLayout) { l.Output.Width = 16385 }},
		{"too many", func(l *nativeLayout) {
			for len(l.Windows) < 9 {
				l.Windows = append(l.Windows, nativeTile{ID: strings.Repeat("c", 31) + string(rune('0'+len(l.Windows))), Width: 1, Height: 1})
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := nativeTestLayout()
			tc.edit(&l)
			if err := validateNativeLayout(l); err == nil {
				t.Fatal("accepted malformed layout")
			}
		})
	}
}

func TestNativeLayoutMapsNegativeMonitorOriginAndAspect(t *testing.T) {
	client := seamlessRect{-1600, 40, -320, 760}
	l := nativeTestLayout()
	tile := nativeTile{X: 640, Y: 360, Width: 1280, Height: 720}
	if !nativeAspectMatches(l.Output.Width, l.Output.Height, client) {
		t.Fatal("matching output aspect rejected")
	}
	if got := nativeScaleTile(tile, l.Output.Width, l.Output.Height, client); got != (seamlessRect{-1280, 220, -640, 580}) {
		t.Fatalf("mapped tile: %+v", got)
	}
	if nativeAspectMatches(1920, 1080, seamlessRect{0, 0, 800, 600}) {
		t.Fatal("letterboxed output accepted")
	}
}

func TestNativePresentationRequiresBearerAndDisablesCaptureInput(t *testing.T) {
	backend := &fakeSeamlessBackend{items: []seamlessWindow{{PID: 123, handle: 0xab, Class: "Editor", Width: 800, Height: 600}}}
	b := &seamlessWindowBridge{token: strings.Repeat("a", 64), backend: backend, frames: make(chan struct{}, 2)}
	request := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("Authorization", "Bearer "+b.token)
		}
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	if r := request(http.MethodGet, "/v1/presentation", "", false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized mode: %d", r.Code)
	}
	if r := request(http.MethodGet, "/v1/presentation", "", true); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"mode":"capture"`) {
		t.Fatalf("capture mode: %d %s", r.Code, r.Body.String())
	}
	if r := request(http.MethodPost, "/v1/layout", `{}`, true); r.Code != http.StatusConflict {
		t.Fatalf("layout accepted in capture mode: %d", r.Code)
	}
	b.projection = &nativeProjection{bridge: b, tracked: make(map[seamlessWindowKey]*nativeProjectedWindow)}
	if r := request(http.MethodGet, "/v1/presentation", "", true); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"mode":"native"`) {
		t.Fatalf("native mode: %d %s", r.Code, r.Body.String())
	}
	if !b.grantWindow(backend.items[0]) {
		t.Fatal("host grant failed")
	}
	id := b.catalogue()[0].ID
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusConflict {
		t.Fatalf("native frame accepted: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/windows/"+id+"/input", `{"type":"key","vk":65}`, true); r.Code != http.StatusConflict || backend.inputCount != 0 {
		t.Fatalf("native input accepted: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", `{"sequence":1,"output":{"width":0,"height":1},"windows":[]}`, true); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed dimensions: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]} {}`, true); r.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON accepted: %d", r.Code)
	}
	empty := `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]}`
	if r := request(http.MethodPost, "/v1/layout", empty, true); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"accepted":true`) {
		t.Fatalf("empty release rejected: %d %s", r.Code, r.Body.String())
	}
	if r := request(http.MethodPost, "/v1/layout", empty, true); r.Code != http.StatusConflict {
		t.Fatalf("stale sequence accepted: %d", r.Code)
	}
}

func TestNativeRestoreKeepsSnapshotAcrossFailures(t *testing.T) {
	key := seamlessWindowKey{pid: 42, handle: 0x1234}
	state := &nativeProjectedWindow{pendingRestore: true}
	tracked := map[seamlessWindowKey]*nativeProjectedWindow{key: state}
	attempts := 0
	release := func(*nativeProjectedWindow) bool { attempts++; return attempts == 3 }
	retryNativeRestores(tracked, release)
	if len(tracked) != 1 || state.restoreAttempts != 1 {
		t.Fatal("lost placement snapshot after transient failure")
	}
	retryNativeRestores(tracked, release)
	if len(tracked) != 1 || state.restoreAttempts != 2 {
		t.Fatal("lost placement snapshot after second failure")
	}
	retryNativeRestores(tracked, release)
	if len(tracked) != 0 {
		t.Fatal("restored placement snapshot was retained")
	}
	state = &nativeProjectedWindow{pendingRestore: true}
	tracked[key] = state
	for i := 0; i < 8; i++ {
		retryNativeRestores(tracked, func(*nativeProjectedWindow) bool { return false })
	}
	if len(tracked) != 1 || state.restoreAttempts != 3 {
		t.Fatal("restore retry was unbounded or discarded original placement")
	}
}

func TestNativeGrantLossClearsForegroundRouting(t *testing.T) {
	w := seamlessWindow{PID: 9, handle: 0x987, Class: "OwnedApp"}
	key := seamlessKey(w)
	b := &seamlessWindowBridge{grants: map[seamlessWindowKey]string{key: w.Class}}
	p := &nativeProjection{bridge: b, tracked: map[seamlessWindowKey]*nativeProjectedWindow{
		key: {window: w},
	}, deadline: time.Now().Add(time.Second)}
	p.foreground.Store(&nativeForegroundSnapshot{until: p.deadline, count: 1, windows: [8]nativeForegroundWindow{{key: key}}})
	delete(b.grants, key) // Catalogue/tray invalidated the former grant.
	p.mu.Lock()
	changed := p.auditGrantsLocked()
	p.retryRestoreLocked()
	if changed {
		p.publishForegroundLocked()
	}
	p.mu.Unlock()
	if !changed || len(p.tracked) != 0 {
		t.Fatal("revoked native window was retained")
	}
	if s := p.foreground.Load(); s == nil || s.count != 0 {
		t.Fatal("revoked native window still eligible for Super routing")
	}
}
