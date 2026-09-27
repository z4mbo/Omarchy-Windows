//go:build windows

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type blockingNativeInputBackend struct {
	fakeSeamlessBackend
	entered chan struct{}
	release chan struct{}
}

func (b *blockingNativeInputBackend) input(window seamlessWindow, event seamlessInput) error {
	close(b.entered)
	<-b.release
	return b.fakeSeamlessBackend.input(window, event)
}

func nativeTestLayout() nativeLayout {
	var l nativeLayout
	l.Sequence = 1
	l.Output.Width, l.Output.Height = 2560, 1440
	l.Windows = []nativeTile{{ID: strings.Repeat("a", 32), X: 100, Y: 200, Width: 400, Height: 300, Visible: true},
		{ID: strings.Repeat("b", 32), X: 0, Y: 0, Width: 1, Height: 1, Visible: false}}
	return l
}

func TestNativeHeartbeatKeepsAppResizeButRepairsLostVisibility(t *testing.T) {
	wanted := seamlessRect{100, 200, 900, 700}
	appResized := seamlessRect{100, 200, 1100, 800}
	state := &nativeProjectedWindow{
		applied: true, requestedShown: true, requestedRect: wanted,
		lastRect: appResized, lastVisible: true,
	}
	tile := nativeTile{Visible: true}
	if !nativeCanKeepPlacement(state, tile, wanted, true) {
		t.Fatal("app-chosen resize should not be reset on a visible heartbeat")
	}
	if nativeCanKeepPlacement(state, tile, wanted, false) {
		t.Fatal("a hidden native window must be shown again for a visible guest tile")
	}
	if nativeCanKeepPlacement(state, tile, seamlessRect{200, 200, 1000, 700}, true) {
		t.Fatal("a new guest tile position must be applied")
	}
	tile.Visible = false
	if nativeCanKeepPlacement(state, tile, wanted, false) {
		t.Fatal("first request to hide the native window must be applied")
	}
	state.requestedShown = false
	if !nativeCanKeepPlacement(state, tile, wanted, false) {
		t.Fatal("an already hidden native window should stay hidden")
	}
	if nativeCanKeepPlacement(state, tile, wanted, true) {
		t.Fatal("an app-shown window must be hidden again for a hidden guest tile")
	}
}

func TestTrayReconciliationRetainsHiddenNativeGrant(t *testing.T) {
	w := seamlessWindow{PID: 4711, handle: 0xabc, Class: "OwnedEditor", Title: "Editor"}
	backend := &fakeSeamlessBackend{items: []seamlessWindow{w}}
	b := &seamlessWindowBridge{token: strings.Repeat("d", 64), backend: backend,
		grants: map[seamlessWindowKey]seamlessGrant{seamlessKey(w): {class: w.Class}}}
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

func TestNativeFullscreenIntentIgnoresProjectorGeometry(t *testing.T) {
	projected := seamlessRect{100, 200, 900, 700}
	fullscreen := seamlessRect{0, 0, 2560, 1440}
	state := &nativeProjectedWindow{fullscreenIntent: true, lastRect: projected, applied: true}
	if got := nativeResolvedFullscreen(state, projected, true, true, false); !got {
		t.Fatal("projector resize cleared the app's fullscreen intent")
	}
	state.fullscreenIntent = false
	state.lastRect = fullscreen
	if got := nativeResolvedFullscreen(state, fullscreen, true, true, true); got {
		t.Fatal("projector-owned monitor geometry invented app fullscreen")
	}
	state.lastRect = projected
	if got := nativeResolvedFullscreen(state, fullscreen, false, true, true); got {
		t.Fatal("hiding the projected window changed fullscreen intent")
	}
	if got := nativeResolvedFullscreen(state, fullscreen, true, false, true); got {
		t.Fatal("unmeasured app geometry changed fullscreen intent")
	}
	state.uncertainMutation = true
	if got := nativeResolvedFullscreen(state, fullscreen, true, true, true); got {
		t.Fatal("uncertain projector mutation was classified as app fullscreen")
	}
}

func TestNativeFullscreenIntentAcceptsIndependentAppResize(t *testing.T) {
	projected := seamlessRect{100, 200, 900, 700}
	fullscreen := seamlessRect{0, 0, 2560, 1440}
	state := &nativeProjectedWindow{lastRect: projected, applied: true}
	if got := nativeResolvedFullscreen(state, fullscreen, true, true, true); !got {
		t.Fatal("independent app fullscreen transition was missed")
	}
	state.fullscreenIntent = true
	state.lastRect = fullscreen
	if got := nativeResolvedFullscreen(state, projected, true, true, false); got {
		t.Fatal("independent app return to windowed geometry was missed")
	}
	state.applied = false // Snapshot exists but the first projection has not run.
	if got := nativeResolvedFullscreen(state, projected, true, true, false); got {
		t.Fatal("app resize before first projection was missed")
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
		{"occlusion negative", func(l *nativeLayout) { l.Windows[0].Occlusions = []nativeOcclusion{{X: -1, Width: 1, Height: 1}} }},
		{"occlusion outside tile", func(l *nativeLayout) { l.Windows[0].Occlusions = []nativeOcclusion{{X: 399, Width: 2, Height: 1}} }},
		{"occlusion zero height", func(l *nativeLayout) { l.Windows[0].Occlusions = []nativeOcclusion{{Width: 1}} }},
		{"hidden occlusion", func(l *nativeLayout) { l.Windows[1].Occlusions = []nativeOcclusion{{Width: 1, Height: 1}} }},
		{"too many occlusions", func(l *nativeLayout) { l.Windows[0].Occlusions = make([]nativeOcclusion, nativeMaxOcclusions+1) }},
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

func TestNativeOcclusionsScaleInTileLocalCoordinates(t *testing.T) {
	l := nativeTestLayout()
	tile := l.Windows[0]
	tile.Occlusions = []nativeOcclusion{{X: 0, Y: 0, Width: 100, Height: 50},
		{X: 50, Y: 20, Width: 100, Height: 50}} // Overlap is an intentional bounded union.
	if err := validateNativeLayout(nativeLayout{Sequence: l.Sequence, Output: l.Output, Windows: []nativeTile{tile}}); err != nil {
		t.Fatal(err)
	}
	client := seamlessRect{-1600, 40, -320, 760} // Half-scale output at a negative monitor origin.
	window := nativeScaleTile(tile, l.Output.Width, l.Output.Height, client)
	got := nativeScaleOcclusions(tile, l.Output.Width, l.Output.Height, client, window)
	want := []seamlessRect{{0, 0, 50, 25}, {25, 10, 75, 35}}
	if len(got) != len(want) {
		t.Fatalf("scaled occlusion count: %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scaled occlusion %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := nativeScaleOcclusions(nativeTile{X: tile.X, Y: tile.Y, Width: tile.Width, Height: tile.Height,
		Occlusions: []nativeOcclusion{{X: 1, Y: 1, Width: 1, Height: 1}}},
		l.Output.Width, l.Output.Height, client, window); len(got) != 0 {
		t.Fatalf("zero physical-pixel occlusion must not mutate region: %+v", got)
	}
}

func TestNativeOcclusionUnionHidesOnlyWhenFullyCovered(t *testing.T) {
	tile := nativeTile{Width: 100, Height: 80, Visible: true,
		Occlusions: []nativeOcclusion{{X: 0, Y: 0, Width: 60, Height: 80},
			{X: 50, Y: 0, Width: 50, Height: 80}}}
	if !nativeOcclusionsCoverTile(tile) {
		t.Fatal("overlapping rectangles whose union covers the tile were not recognized")
	}
	tile.Occlusions[1].Width = 49
	if nativeOcclusionsCoverTile(tile) {
		t.Fatal("one-pixel uncovered strip was ignored")
	}
	tile.Occlusions = nil
	if nativeOcclusionsCoverTile(tile) {
		t.Fatal("empty occlusions were treated as a hidden tile")
	}
}

func TestNativeOcclusionNeverClipsAnAppOwnedResize(t *testing.T) {
	tile := nativeTile{Visible: true}
	expected := seamlessRect{100, 200, 500, 500}
	if !nativeOcclusionGeometryReady(tile, expected, expected, true) {
		t.Fatal("matching projected geometry was not eligible for clipping")
	}
	if nativeOcclusionGeometryReady(tile, expected, seamlessRect{100, 200, 900, 700}, true) {
		t.Fatal("app-owned resize would receive stale tile clipping")
	}
	if nativeOcclusionGeometryReady(tile, expected, expected, false) {
		t.Fatal("hidden app would receive stale tile clipping")
	}
	tile.Visible = false
	if nativeOcclusionGeometryReady(tile, expected, expected, true) {
		t.Fatal("guest-hidden tile would receive clipping")
	}
}

func TestNativeRegionRejectsRTLCoordinateMode(t *testing.T) {
	if !nativeRegionStyleSupported(0) || !nativeRegionStyleSupported(0x00040000) {
		t.Fatal("ordinary LTR window style rejected")
	}
	if nativeRegionStyleSupported(0x00400000) || nativeRegionStyleSupported(0x00440000) {
		t.Fatal("RTL window would receive left-origin region coordinates")
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
	if !b.grantWindow(backend.items[0]) {
		t.Fatal("host grant failed")
	}
	id := b.catalogue()[0].ID
	// A revision-33 guest never negotiates presentation or sends layouts.
	// Configuring a newer host for native must not break its capture path.
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusOK || r.Body.String() != "png-bytes" {
		t.Fatalf("old guest capture failed before native layout: %d %s", r.Code, r.Body.String())
	}
	if r := request(http.MethodPost, "/v1/windows/"+id+"/input", `{"type":"key","vk":65}`, true); r.Code != http.StatusOK || backend.inputCount != 1 {
		t.Fatalf("old guest input failed before native layout: %d", r.Code)
	}
	if r := request(http.MethodGet, "/v1/presentation", "", true); r.Code != http.StatusOK ||
		!strings.Contains(r.Body.String(), `"mode":"native"`) ||
		!strings.Contains(r.Body.String(), `"occlusions":{"coordinates":"tile","maxRectsPerWindow":16}`) {
		t.Fatalf("native mode: %d %s", r.Code, r.Body.String())
	}
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusOK {
		t.Fatalf("mode query activated native before a layout: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]}`, false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized layout response: %d", r.Code)
	}
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusOK {
		t.Fatalf("unauthorized layout activated native: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", `{"sequence":1,"output":{"width":0,"height":1},"windows":[]}`, true); r.Code != http.StatusBadRequest {
		t.Fatalf("malformed dimensions: %d", r.Code)
	}
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusOK {
		t.Fatalf("invalid layout activated native: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]} {}`, true); r.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON accepted: %d", r.Code)
	}
	empty := `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]}`
	if r := request(http.MethodPost, "/v1/layout", empty, true); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), `"accepted":true`) {
		t.Fatalf("empty release rejected: %d %s", r.Code, r.Body.String())
	}
	if r := request(http.MethodGet, "/v1/windows/"+id+"/frame", "", true); r.Code != http.StatusConflict {
		t.Fatalf("native frame accepted after layout: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/windows/"+id+"/input", `{"type":"key","vk":65}`, true); r.Code != http.StatusConflict || backend.inputCount != 1 {
		t.Fatalf("native input accepted after layout: %d", r.Code)
	}
	if r := request(http.MethodPost, "/v1/layout", empty, true); r.Code != http.StatusConflict {
		t.Fatalf("stale sequence accepted: %d", r.Code)
	}
}

func TestNativeActivationWaitsForLegacyInput(t *testing.T) {
	window := seamlessWindow{PID: 123, handle: 0xab, Class: "Editor", Width: 800, Height: 600}
	backend := &blockingNativeInputBackend{fakeSeamlessBackend: fakeSeamlessBackend{items: []seamlessWindow{window}},
		entered: make(chan struct{}), release: make(chan struct{})}
	b := &seamlessWindowBridge{token: strings.Repeat("b", 64), backend: backend, frames: make(chan struct{}, 2)}
	b.projection = &nativeProjection{bridge: b, tracked: make(map[seamlessWindowKey]*nativeProjectedWindow)}
	if !b.grantWindow(window) {
		t.Fatal("host grant failed")
	}
	id := b.catalogue()[0].ID
	request := func(path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+b.token)
		w := httptest.NewRecorder()
		b.ServeHTTP(w, r)
		return w
	}
	inputDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { inputDone <- request("/v1/windows/"+id+"/input", `{"type":"key","vk":65}`) }()
	select {
	case <-backend.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy input did not start")
	}
	layoutStarted := make(chan struct{})
	layoutDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		close(layoutStarted)
		layoutDone <- request("/v1/layout", `{"sequence":1,"output":{"width":2560,"height":1440},"windows":[]}`)
	}()
	<-layoutStarted
	select {
	case <-layoutDone:
		t.Fatal("native activation overlapped in-flight legacy input")
	case <-time.After(100 * time.Millisecond):
	}
	close(backend.release)
	select {
	case got := <-inputDone:
		if got.Code != http.StatusOK {
			t.Fatalf("legacy input failed while activation waited: %d", got.Code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("legacy input did not drain")
	}
	select {
	case got := <-layoutDone:
		if got.Code != http.StatusOK {
			t.Fatalf("native activation failed after legacy input drained: %d %s", got.Code, got.Body.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native activation did not finish")
	}
	if got := request("/v1/windows/"+id+"/input", `{"type":"key","vk":65}`); got.Code != http.StatusConflict || backend.inputCount != 1 {
		t.Fatalf("legacy input continued after native activation: %d", got.Code)
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
	base := time.Now()
	state.nextRestore = base.Add(time.Second)
	retryNativeRestoresAt(tracked, func(*nativeProjectedWindow) bool {
		t.Fatal("backoff ignored before retry time")
		return false
	}, base.Add(500*time.Millisecond))
	retryNativeRestoresAt(tracked, func(*nativeProjectedWindow) bool { return true }, base.Add(time.Second))
	if len(tracked) != 0 {
		t.Fatal("recovered window never retried after backoff")
	}
}

func TestNativeGrantLossClearsForegroundRouting(t *testing.T) {
	w := seamlessWindow{PID: 9, handle: 0x987, Class: "OwnedApp"}
	key := seamlessKey(w)
	b := &seamlessWindowBridge{grants: map[seamlessWindowKey]seamlessGrant{key: {class: w.Class}}}
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
