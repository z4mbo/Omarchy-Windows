//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

const nativeProjectionLease = 3 * time.Second
const nativeMaxOcclusions = 16

type nativeLayout struct {
	Sequence int64 `json:"sequence"`
	Output   struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"output"`
	Windows []nativeTile `json:"windows"`
}

type nativeTile struct {
	ID         string            `json:"id"`
	X          int               `json:"x"`
	Y          int               `json:"y"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	Visible    bool              `json:"visible"`
	Occlusions []nativeOcclusion `json:"occlusions,omitempty"`
}

// Occlusions are tile-local guest pixels. They are optional in protocol 1.
type nativeOcclusion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

func validateNativeLayout(l nativeLayout) error {
	if l.Sequence <= 0 || l.Output.Width < 64 || l.Output.Height < 64 ||
		l.Output.Width > 16384 || l.Output.Height > 16384 || len(l.Windows) > 8 {
		return errors.New("invalid native layout size or sequence")
	}
	seen := make(map[string]bool, len(l.Windows))
	for _, tile := range l.Windows {
		if len(tile.ID) != 32 || strings.Trim(tile.ID, "0123456789abcdef") != "" || seen[tile.ID] ||
			tile.X < 0 || tile.Y < 0 || tile.Width < 1 || tile.Height < 1 ||
			tile.X > l.Output.Width || tile.Y > l.Output.Height ||
			tile.Width > l.Output.Width-tile.X || tile.Height > l.Output.Height-tile.Y {
			return errors.New("invalid native window tile")
		}
		if len(tile.Occlusions) > nativeMaxOcclusions || (!tile.Visible && len(tile.Occlusions) != 0) {
			return errors.New("invalid native window occlusions")
		}
		for _, clip := range tile.Occlusions {
			if clip.X < 0 || clip.Y < 0 || clip.Width < 1 || clip.Height < 1 ||
				clip.X >= tile.Width || clip.Y >= tile.Height ||
				clip.Width > tile.Width-clip.X || clip.Height > tile.Height-clip.Y {
				return errors.New("invalid native window occlusion")
			}
		}
		seen[tile.ID] = true
	}
	return nil
}

// All operations are serialized under mu. In particular no queued placement
// may run after a later revoke or lease expiry. Win32 placement calls here are
// synchronous; a hung target is rejected before mutation.
type nativeProjection struct {
	bridge                    *seamlessWindowBridge
	mu                        sync.Mutex
	tracked                   map[seamlessWindowKey]*nativeProjectedWindow
	sequence                  int64
	deadline                  time.Time
	closed                    bool
	stop                      chan struct{}
	foreground                atomic.Pointer[nativeForegroundSnapshot]
	hiddenWindowSourceForTest func([]seamlessWindow) []seamlessWindow
}

type nativeForegroundSnapshot struct {
	until   time.Time
	windows [8]nativeForegroundWindow
	count   int
}

type nativeForegroundWindow struct {
	key  seamlessWindowKey
	rect seamlessRect
}

type nativeProjectedWindow struct {
	window            seamlessWindow
	created           uint64
	original          windowPlacementStruct
	originalVisible   bool
	restorePlacement  windowPlacementStruct
	restoreVisible    bool
	lastRect          seamlessRect
	lastVisible       bool
	requestedRect     seamlessRect
	requestedShown    bool
	applied           bool
	pendingRestore    bool
	restoreAttempts   int
	nextRestore       time.Time
	restoreStarted    bool
	restoreRect       seamlessRect
	restoreShown      bool
	uncertainMutation bool
	// SetWindowRgn transfers ownership of the region. We retain our own copy
	// to detect a later application-owned region change before restoring NULL.
	lastRegion uintptr
}

var (
	nativeGetAncestor         = user32.NewProc("GetAncestor")
	nativeClientToScreen      = user32.NewProc("ClientToScreen")
	nativeGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	nativeGetProcessTimes     = kernel32.NewProc("GetProcessTimes")
	nativeGetWindowLongPtr    = user32.NewProc("GetWindowLongPtrW")
	nativeGetWindow           = user32.NewProc("GetWindow")
	nativeGetWindowRgn        = user32.NewProc("GetWindowRgn")
	nativeSetWindowRgn        = user32.NewProc("SetWindowRgn")
	nativeGDI32               = syscall.NewLazyDLL("gdi32.dll")
	nativeCreateRectRgn       = nativeGDI32.NewProc("CreateRectRgn")
	nativeCombineRgn          = nativeGDI32.NewProc("CombineRgn")
	nativeEqualRgn            = nativeGDI32.NewProc("EqualRgn")
	nativeDeleteObject        = nativeGDI32.NewProc("DeleteObject")
	nativeSetThreadDPI        = user32.NewProc("SetThreadDpiAwarenessContext")
	nativeQemuEnumMu          sync.Mutex
	nativeQemuEnumPID         uint32
	nativeQemuEnumCount       int
	nativeQemuEnumHWND        uintptr
	nativeQemuEnumCallback    = syscall.NewCallback(nativeCountQemuWindow)
)

// Keep all host rect reads and writes in physical per-monitor coordinates.
// The launcher as a whole keeps its existing DPI behavior.
func nativeDPIEnter() (func(), error) {
	runtime.LockOSThread()
	previous, _, err := nativeSetThreadDPI.Call(^uintptr(3)) // PER_MONITOR_AWARE_V2 = -4
	if previous == 0 {
		runtime.UnlockOSThread()
		return nil, fmt.Errorf("per-monitor DPI context unavailable: %w", err)
	}
	return func() { nativeSetThreadDPI.Call(previous); runtime.UnlockOSThread() }, nil
}

func nativeCountQemuWindow(hwnd, _ uintptr) uintptr {
	if isQemuDisplayWindow(hwnd, nativeQemuEnumPID) {
		if visible, _, _ := procIsWindowVisible.Call(hwnd); visible != 0 {
			nativeQemuEnumCount++
			nativeQemuEnumHWND = hwnd
		}
	}
	return 1
}

func newNativeProjection(b *seamlessWindowBridge) *nativeProjection {
	p := &nativeProjection{bridge: b, tracked: make(map[seamlessWindowKey]*nativeProjectedWindow), stop: make(chan struct{})}
	go p.watch()
	return p
}

func (p *nativeProjection) watch() {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			restore, err := nativeDPIEnter()
			if err != nil {
				continue
			}
			p.mu.Lock()
			grantChanged := p.auditGrantsLocked()
			if !p.deadline.IsZero() && time.Now().After(p.deadline) {
				p.foreground.Store(nil)
				p.markAllRestoreLocked()
				p.deadline = time.Time{}
			}
			p.retryRestoreLocked()
			if grantChanged {
				p.publishForegroundLocked()
			}
			p.mu.Unlock()
			restore()
		}
	}
}

func (p *nativeProjection) auditGrantsLocked() bool {
	changed := false
	p.bridge.mu.Lock()
	for key, state := range p.tracked {
		if class, ok := p.bridge.grants[key]; !ok || class != state.window.Class {
			if !state.pendingRestore {
				state.pendingRestore = true
				changed = true
			}
		}
	}
	p.bridge.mu.Unlock()
	return changed
}

func (p *nativeProjection) Close() {
	restore, err := nativeDPIEnter()
	if err != nil {
		logf("native projection DPI unavailable on close: %v", err)
		return
	}
	defer restore()
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.stop)
		p.foreground.Store(nil)
		p.markAllRestoreLocked()
		for i := 0; i < 3 && len(p.tracked) > 0; i++ {
			for _, state := range p.tracked {
				state.nextRestore = time.Time{}
			}
			p.retryRestoreLocked()
			if len(p.tracked) > 0 {
				time.Sleep(100 * time.Millisecond)
			}
		}
		if len(p.tracked) > 0 {
			logf("native placement could not be restored for %d window(s)", len(p.tracked))
		}
	}
	p.mu.Unlock()
}

func (p *nativeProjection) Release(window seamlessWindow) {
	restore, err := nativeDPIEnter()
	if err != nil {
		logf("native projection DPI unavailable on revoke: %v", err)
		return
	}
	defer restore()
	p.mu.Lock()
	defer p.mu.Unlock()
	key := seamlessKey(window)
	if state := p.tracked[key]; state != nil {
		state.pendingRestore = true
		state.restoreAttempts = 0
		state.nextRestore = time.Time{}
		if p.releaseLocked(state) {
			delete(p.tracked, key)
		}
		p.publishForegroundLocked()
	}
}

func (p *nativeProjection) HiddenWindows(visible []seamlessWindow) []seamlessWindow {
	if p.hiddenWindowSourceForTest != nil {
		return p.hiddenWindowSourceForTest(visible)
	}
	restore, err := nativeDPIEnter()
	if err != nil {
		return nil
	}
	defer restore()
	p.mu.Lock()
	defer p.mu.Unlock()
	known := make(map[seamlessWindowKey]bool, len(visible))
	for _, w := range visible {
		known[seamlessKey(w)] = true
	}
	var hidden []seamlessWindow
	for key, state := range p.tracked {
		if state.pendingRestore {
			continue
		}
		if known[key] {
			continue
		}
		if !nativeIdentityMatches(state) {
			nativeFreeRegion(state)
			delete(p.tracked, key)
			continue
		}
		if shown, _, _ := procIsWindowVisible.Call(key.handle); shown == 0 {
			hidden = append(hidden, state.window)
		}
	}
	return hidden
}

func nativeProcessCreated(pid uint32) (uint64, error) {
	h, err := syscall.OpenProcess(0x1000, false, pid) // PROCESS_QUERY_LIMITED_INFORMATION
	if err != nil {
		return 0, err
	}
	defer syscall.CloseHandle(h)
	var creation, exit, kernel, user syscall.Filetime
	r, _, callErr := nativeGetProcessTimes.Call(uintptr(h), uintptr(unsafe.Pointer(&creation)),
		uintptr(unsafe.Pointer(&exit)), uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if r == 0 {
		return 0, callErr
	}
	return uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime), nil
}

func nativeIdentityMatches(state *nativeProjectedWindow) bool {
	w := state.window
	if !seamlessWindowStillMatches(w) {
		return false
	}
	root, _, _ := nativeGetAncestor.Call(w.handle, 2) // GA_ROOT; never parent another process's window
	if root != w.handle {
		return false
	}
	var name [128]uint16
	seamlessGetClass.Call(w.handle, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if syscall.UTF16ToString(name[:]) != w.Class {
		return false
	}
	created, err := nativeProcessCreated(w.PID)
	return err == nil && created == state.created
}

func nativeSnapshot(w seamlessWindow) (*nativeProjectedWindow, error) {
	if !seamlessWindowStillMatches(w) || w.PID == 0 || w.PID == qemuPid.Load() || w.PID == uint32(os.Getpid()) {
		return nil, errors.New("window identity changed")
	}
	root, _, _ := nativeGetAncestor.Call(w.handle, 2)
	if root != w.handle {
		return nil, errors.New("window is not top-level")
	}
	style, _, _ := nativeGetWindowLongPtr.Call(w.handle, ^uintptr(19)) // GWL_EXSTYLE
	if style&0x8 != 0 {
		return nil, errors.New("topmost window cannot be projected safely")
	}
	var name [128]uint16
	seamlessGetClass.Call(w.handle, uintptr(unsafe.Pointer(&name[0])), uintptr(len(name)))
	if syscall.UTF16ToString(name[:]) != w.Class {
		return nil, errors.New("window class changed")
	}
	created, err := nativeProcessCreated(w.PID)
	if err != nil {
		return nil, err
	}
	var placement windowPlacementStruct
	placement.length = uint32(unsafe.Sizeof(placement))
	if ok, _, _ := procGetWindowPlacement.Call(w.handle, uintptr(unsafe.Pointer(&placement))); ok == 0 {
		return nil, errors.New("cannot read window placement")
	}
	var rect seamlessRect
	if ok, _, _ := seamlessGetRect.Call(w.handle, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return nil, errors.New("cannot read window rectangle")
	}
	shown, _, _ := procIsWindowVisible.Call(w.handle)
	return &nativeProjectedWindow{window: w, created: created, original: placement,
		originalVisible: shown != 0, restorePlacement: placement, restoreVisible: shown != 0,
		lastRect: rect, lastVisible: shown != 0}, nil
}

func nativeQemuClientRect() (seamlessRect, error) {
	hwnd, pid := qemuHwnd.Load(), qemuPid.Load()
	if hwnd == 0 || !isQemuDisplayWindow(hwnd, pid) {
		return seamlessRect{}, errors.New("QEMU window unavailable")
	}
	nativeQemuEnumMu.Lock()
	nativeQemuEnumPID, nativeQemuEnumCount, nativeQemuEnumHWND = pid, 0, 0
	procEnumWindows.Call(nativeQemuEnumCallback, 0)
	count, only := nativeQemuEnumCount, nativeQemuEnumHWND
	nativeQemuEnumMu.Unlock()
	if count != 1 || only != hwnd {
		return seamlessRect{}, errors.New("native projection requires one QEMU display")
	}
	if visible, _, _ := procIsWindowVisible.Call(hwnd); visible == 0 {
		return seamlessRect{}, errors.New("QEMU is hidden")
	}
	var placement windowPlacementStruct
	placement.length = uint32(unsafe.Sizeof(placement))
	if ok, _, _ := procGetWindowPlacement.Call(hwnd, uintptr(unsafe.Pointer(&placement))); ok == 0 || placement.showCmd == swShowMinimized {
		return seamlessRect{}, errors.New("QEMU is minimized")
	}
	// A topmost QEMU cannot be safely overlaid by ordinary native HWNDs.
	style, _, _ := nativeGetWindowLongPtr.Call(hwnd, ^uintptr(19)) // GWL_EXSTYLE = -20
	if style&0x8 != 0 {
		return seamlessRect{}, errors.New("QEMU is topmost")
	}
	var client seamlessRect
	if ok, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&client))); ok == 0 {
		return seamlessRect{}, errors.New("QEMU client unavailable")
	}
	var origin seamlessPoint
	if ok, _, _ := nativeClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&origin))); ok == 0 {
		return seamlessRect{}, errors.New("QEMU origin unavailable")
	}
	width, height := client.right-client.left, client.bottom-client.top
	if width < 64 || height < 64 || width > 16384 || height > 16384 {
		return seamlessRect{}, errors.New("QEMU client size invalid")
	}
	return seamlessRect{origin.x, origin.y, origin.x + width, origin.y + height}, nil
}

func nativeScaleTile(tile nativeTile, outputWidth, outputHeight int, client seamlessRect) seamlessRect {
	scale := func(n, source, target int) int32 {
		return int32((int64(n)*int64(target) + int64(source)/2) / int64(source))
	}
	x0 := client.left + scale(tile.X, outputWidth, int(client.right-client.left))
	y0 := client.top + scale(tile.Y, outputHeight, int(client.bottom-client.top))
	x1 := client.left + scale(tile.X+tile.Width, outputWidth, int(client.right-client.left))
	y1 := client.top + scale(tile.Y+tile.Height, outputHeight, int(client.bottom-client.top))
	return seamlessRect{x0, y0, x1, y1}
}

func nativeScaleOcclusions(tile nativeTile, outputWidth, outputHeight int, client, window seamlessRect) []seamlessRect {
	clips := make([]seamlessRect, 0, len(tile.Occlusions))
	for _, clip := range tile.Occlusions {
		mapped := nativeScaleTile(nativeTile{X: tile.X + clip.X, Y: tile.Y + clip.Y,
			Width: clip.Width, Height: clip.Height}, outputWidth, outputHeight, client)
		mapped.left -= window.left
		mapped.right -= window.left
		mapped.top -= window.top
		mapped.bottom -= window.top
		if mapped.right > mapped.left && mapped.bottom > mapped.top {
			clips = append(clips, mapped)
		}
	}
	return clips
}

// The host hides a fully covered HWND instead of leaving an invisible but
// technically shown window eligible for foreground keyboard routing.
func nativeOcclusionsCoverTile(tile nativeTile) bool {
	if len(tile.Occlusions) == 0 {
		return false
	}
	xs, ys := []int{0, tile.Width}, []int{0, tile.Height}
	for _, clip := range tile.Occlusions {
		xs = append(xs, clip.X, clip.X+clip.Width)
		ys = append(ys, clip.Y, clip.Y+clip.Height)
	}
	sort.Ints(xs)
	sort.Ints(ys)
	for xi := 1; xi < len(xs); xi++ {
		if xs[xi] == xs[xi-1] {
			continue
		}
		for yi := 1; yi < len(ys); yi++ {
			if ys[yi] == ys[yi-1] {
				continue
			}
			covered := false
			for _, clip := range tile.Occlusions {
				if clip.X <= xs[xi-1] && clip.X+clip.Width >= xs[xi] &&
					clip.Y <= ys[yi-1] && clip.Y+clip.Height >= ys[yi] {
					covered = true
					break
				}
			}
			if !covered {
				return false
			}
		}
	}
	return true
}

func nativeFreeRegion(state *nativeProjectedWindow) {
	if state.lastRegion != 0 {
		nativeDeleteObject.Call(state.lastRegion)
		state.lastRegion = 0
	}
}

func nativeRegionMatches(hwnd, expected uintptr) (bool, error) {
	actual, _, _ := nativeCreateRectRgn.Call(0, 0, 0, 0)
	if actual == 0 {
		return false, errors.New("cannot query window region")
	}
	defer nativeDeleteObject.Call(actual)
	kind, _, _ := nativeGetWindowRgn.Call(hwnd, actual)
	if kind == 0 { // ERROR: no region, or an application-owned reset.
		return false, nil
	}
	equal, _, _ := nativeEqualRgn.Call(actual, expected)
	return equal == 1, nil
}

func nativeRestoreRegion(state *nativeProjectedWindow) bool {
	if state.lastRegion == 0 {
		return true
	}
	if !nativeIdentityMatches(state) {
		nativeFreeRegion(state)
		return true
	}
	hwnd := state.window.handle
	matches, err := nativeRegionMatches(hwnd, state.lastRegion)
	if err != nil {
		return false
	}
	if !matches {
		// The application replaced the region. Leave its new shape alone.
		nativeFreeRegion(state)
		return true
	}
	if !nativeIdentityMatches(state) {
		nativeFreeRegion(state)
		return true
	}
	if ok, _, _ := nativeSetWindowRgn.Call(hwnd, 0, 1); ok == 0 {
		return false
	}
	nativeFreeRegion(state)
	return true
}

func nativeApplyRegion(state *nativeProjectedWindow, clips []seamlessRect, window seamlessRect) error {
	if len(clips) == 0 {
		if !nativeRestoreRegion(state) {
			return errors.New("window region restore failed")
		}
		return nil
	}
	if !nativeRegionTargetMatches(state, window) {
		return errors.New("window changed before region update")
	}
	if state.lastRegion != 0 {
		matches, err := nativeRegionMatches(state.window.handle, state.lastRegion)
		if err != nil {
			return err
		}
		if !matches {
			nativeFreeRegion(state)
			return errors.New("application changed its window region")
		}
	} else {
		// A pre-existing custom region can contain holes or other app-owned
		// geometry. Do not replace it in this initial opt-in backend.
		current, _, _ := nativeCreateRectRgn.Call(0, 0, 0, 0)
		if current == 0 {
			return errors.New("cannot query window region")
		}
		kind, _, _ := nativeGetWindowRgn.Call(state.window.handle, current)
		nativeDeleteObject.Call(current)
		if kind != 0 {
			return errors.New("application custom window region is unsupported")
		}
	}
	width, height := window.right-window.left, window.bottom-window.top
	if width < 1 || height < 1 {
		return errors.New("window region dimensions invalid")
	}
	region, _, _ := nativeCreateRectRgn.Call(0, 0, uintptr(width), uintptr(height))
	if region == 0 {
		return errors.New("cannot create window region")
	}
	for _, clip := range clips {
		cut, _, _ := nativeCreateRectRgn.Call(uintptr(clip.left), uintptr(clip.top), uintptr(clip.right), uintptr(clip.bottom))
		if cut == 0 {
			nativeDeleteObject.Call(region)
			return errors.New("cannot create occlusion region")
		}
		kind, _, _ := nativeCombineRgn.Call(region, region, cut, 4) // RGN_DIFF; overlap becomes one bounded union.
		nativeDeleteObject.Call(cut)
		if kind == 0 {
			nativeDeleteObject.Call(region)
			return errors.New("cannot subtract occlusion region")
		}
	}
	if state.lastRegion != 0 {
		equal, _, _ := nativeEqualRgn.Call(region, state.lastRegion)
		if equal == 1 {
			nativeDeleteObject.Call(region)
			if !nativeRegionTargetMatches(state, window) {
				return errors.New("window changed during region update")
			}
			return nil
		}
	}
	copy, _, _ := nativeCreateRectRgn.Call(0, 0, 0, 0)
	if copy == 0 {
		nativeDeleteObject.Call(region)
		return errors.New("cannot copy window region")
	}
	if kind, _, _ := nativeCombineRgn.Call(copy, region, 0, 5); kind == 0 { // RGN_COPY
		nativeDeleteObject.Call(copy)
		nativeDeleteObject.Call(region)
		return errors.New("cannot copy window region")
	}
	if !nativeRegionTargetMatches(state, window) {
		nativeDeleteObject.Call(copy)
		nativeDeleteObject.Call(region)
		return errors.New("window changed before region mutation")
	}
	if ok, _, _ := nativeSetWindowRgn.Call(state.window.handle, region, 1); ok == 0 {
		nativeDeleteObject.Call(copy)
		nativeDeleteObject.Call(region) // Windows owns it only after success.
		return errors.New("window region update failed")
	}
	nativeFreeRegion(state)
	state.lastRegion = copy
	return nil
}

func nativeRegionTargetMatches(state *nativeProjectedWindow, window seamlessRect) bool {
	if !nativeIdentityMatches(state) {
		return false
	}
	style, _, _ := nativeGetWindowLongPtr.Call(state.window.handle, ^uintptr(19)) // GWL_EXSTYLE
	if !nativeRegionStyleSupported(style) {                                       // WS_EX_LAYOUTRTL uses upper-right region coordinates.
		return false
	}
	var actual seamlessRect
	if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&actual))); ok == 0 {
		return false
	}
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	return shown != 0 && actual == window
}

func nativePreflightRegion(state *nativeProjectedWindow) error {
	if !nativeIdentityMatches(state) {
		return errors.New("window identity changed before region preflight")
	}
	style, _, _ := nativeGetWindowLongPtr.Call(state.window.handle, ^uintptr(19)) // GWL_EXSTYLE
	if !nativeRegionStyleSupported(style) {                                       // WS_EX_LAYOUTRTL
		return errors.New("right-to-left window regions are unsupported")
	}
	if state.lastRegion != 0 {
		matches, err := nativeRegionMatches(state.window.handle, state.lastRegion)
		if err != nil {
			return err
		}
		if !matches {
			return errors.New("application changed its window region")
		}
		return nil
	}
	current, _, _ := nativeCreateRectRgn.Call(0, 0, 0, 0)
	if current == 0 {
		return errors.New("cannot query window region")
	}
	kind, _, _ := nativeGetWindowRgn.Call(state.window.handle, current)
	nativeDeleteObject.Call(current)
	if kind != 0 {
		return errors.New("application custom window region is unsupported")
	}
	return nil
}

func nativeRegionStyleSupported(exStyle uintptr) bool {
	return exStyle&0x00400000 == 0 // WS_EX_LAYOUTRTL
}

func nativeOcclusionGeometryReady(tile nativeTile, expected, actual seamlessRect, shown bool) bool {
	return tile.Visible && shown && actual == expected
}

func nativeAspectMatches(outputWidth, outputHeight int, client seamlessRect) bool {
	cw, ch := int64(client.right-client.left), int64(client.bottom-client.top)
	a, b := cw*int64(outputHeight), ch*int64(outputWidth)
	if a < b {
		a, b = b, a
	}
	return a-b <= a/200 // allow only rounding and a small SDL border difference
}

func (p *nativeProjection) Apply(l nativeLayout) (string, error) {
	restore, err := nativeDPIEnter()
	if err != nil {
		return "", err
	}
	defer restore()
	if err := validateNativeLayout(l); err != nil {
		return "", err
	}
	items := p.bridge.catalogue()
	byID := make(map[string]seamlessWindow, len(items))
	for _, w := range items {
		byID[w.ID] = w
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || l.Sequence <= p.sequence {
		return "", errors.New("stale native layout")
	}
	requested := make(map[seamlessWindowKey]nativeTile, len(l.Windows))
	states := make(map[seamlessWindowKey]*nativeProjectedWindow, len(l.Windows))
	// Reuse the scoped DPI setup error variable for snapshot failures.
	for _, tile := range l.Windows {
		w, ok := byID[tile.ID]
		if !ok {
			return "", errors.New("window is not granted")
		}
		key := seamlessKey(w)
		p.bridge.mu.Lock()
		class, granted := p.bridge.grants[key]
		p.bridge.mu.Unlock()
		if !granted || class != w.Class {
			return "", errors.New("window grant changed")
		}
		state := p.tracked[key]
		if state == nil {
			state, err = nativeSnapshot(w)
			if err != nil {
				return "", err
			}
		} else if !nativeIdentityMatches(state) {
			return "", errors.New("window identity changed")
		}
		requested[key] = tile
		states[key] = state
	}
	for key, state := range p.tracked {
		if _, ok := requested[key]; !ok {
			if !state.pendingRestore {
				state.pendingRestore = true
				state.restoreAttempts = 0
				state.nextRestore = time.Time{}
			}
			if p.releaseLocked(state) {
				delete(p.tracked, key)
			}
		}
	}
	// Guest lock/DPMS sends every window as invisible. If the host switches
	// away from QEMU, suspend the projection until this same lease resumes in
	// the foreground, without forcing a host foreground change.
	client, clientErr := nativeQemuClientRect()
	aspectMismatch := clientErr == nil && !nativeAspectMatches(l.Output.Width, l.Output.Height, client)
	if aspectMismatch {
		clientErr = errors.New("QEMU viewport does not match guest output aspect")
	}
	foreground, _, _ := nativeGetForegroundWindow.Call()
	foregroundRoot, _, _ := nativeGetAncestor.Call(foreground, 2)
	active := foregroundRoot == qemuHwnd.Load()
	if !active {
		for key, tile := range requested {
			if key.handle == foregroundRoot && tile.Visible {
				active = true
				break
			}
		}
	}
	suspended := ""
	if clientErr != nil {
		suspended = "display_unavailable"
	}
	if aspectMismatch {
		suspended = "aspect_mismatch"
	}
	if clientErr == nil && !active {
		suspended = "host_inactive"
	}
	if clientErr == nil && active {
		for key, tile := range requested {
			if tile.Visible && len(tile.Occlusions) != 0 && !nativeOcclusionsCoverTile(tile) {
				if err := nativePreflightRegion(states[key]); err != nil {
					return "", err
				}
			}
		}
	}
	for key, tile := range requested {
		state := states[key]
		if state.pendingRestore {
			return "", errors.New("window restoration pending")
		}
		p.tracked[key] = state
		if tile.Visible && nativeOcclusionsCoverTile(tile) {
			tile.Visible = false
			tile.Occlusions = nil
		}
		if clientErr != nil || !active {
			tile.Visible = false
		}
		var rect seamlessRect
		if tile.Visible {
			rect = nativeScaleTile(tile, l.Output.Width, l.Output.Height, client)
		}
		if err := p.placeLocked(state, tile, rect); err != nil {
			p.markAllRestoreLocked()
			p.retryRestoreLocked()
			p.foreground.Store(nil)
			return "", err
		}
		var clips []seamlessRect
		if tile.Visible && len(tile.Occlusions) != 0 {
			var actual seamlessRect
			if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&actual))); ok == 0 {
				p.markAllRestoreLocked()
				p.retryRestoreLocked()
				p.foreground.Store(nil)
				return "", errors.New("window rectangle unavailable for occlusion")
			}
			shown, _, _ := procIsWindowVisible.Call(state.window.handle)
			if nativeOcclusionGeometryReady(tile, rect, actual, shown != 0) {
				clips = nativeScaleOcclusions(tile, l.Output.Width, l.Output.Height, client, actual)
			}
		}
		if err := nativeApplyRegion(state, clips, state.lastRect); err != nil {
			p.markAllRestoreLocked()
			p.retryRestoreLocked()
			p.foreground.Store(nil)
			return "", err
		}
	}
	p.sequence = l.Sequence
	p.deadline = time.Now().Add(nativeProjectionLease)
	p.publishForegroundLocked()
	if !nativeAnyVisible(l.Windows) {
		suspended = ""
	}
	return suspended, nil
}

func nativeAnyVisible(tiles []nativeTile) bool {
	for _, tile := range tiles {
		if tile.Visible {
			return true
		}
	}
	return false
}

func nativeQemuAbove(hwnd uintptr) bool {
	qemu := qemuHwnd.Load()
	for i := 0; i < 512; i++ {
		above, _, _ := nativeGetWindow.Call(hwnd, 3) // GW_HWNDPREV
		if above == 0 {
			return false
		}
		if above == qemu {
			return true
		}
		hwnd = above
	}
	return false
}

func (p *nativeProjection) publishForegroundLocked() {
	if p.deadline.IsZero() || p.closed {
		p.foreground.Store(nil)
		return
	}
	s := &nativeForegroundSnapshot{until: p.deadline}
	for key, state := range p.tracked {
		if s.count == len(s.windows) {
			break
		}
		if state.pendingRestore || !state.applied || !state.lastVisible || !nativeIdentityMatches(state) {
			continue
		}
		var rect seamlessRect
		shown, _, _ := procIsWindowVisible.Call(key.handle)
		if shown != 0 {
			if ok, _, _ := seamlessGetRect.Call(key.handle, uintptr(unsafe.Pointer(&rect))); ok != 0 && rect == state.lastRect {
				s.windows[s.count] = nativeForegroundWindow{key, rect}
				s.count++
			}
		}
	}
	p.foreground.Store(s)
}

// Low-level keyboard hooks may call this without taking the projection lock.
// Only a currently leased, visible, granted top-level app qualifies.
func nativeProjectionForeground() bool {
	restore, err := nativeDPIEnter()
	if err != nil {
		return false
	}
	defer restore()
	b := activeSeamlessBridge.Load()
	if b == nil || b.projection == nil {
		return false
	}
	s := b.projection.foreground.Load()
	if s == nil || !time.Now().Before(s.until) {
		return false
	}
	hwnd, _, _ := nativeGetForegroundWindow.Call()
	root, _, _ := nativeGetAncestor.Call(hwnd, 2)
	for i := 0; i < s.count; i++ {
		key := s.windows[i].key
		if root != key.handle {
			continue
		}
		var pid uint32
		procGetWindowThreadProcessId.Call(root, uintptr(unsafe.Pointer(&pid)))
		if pid != key.pid {
			return false
		}
		var rect seamlessRect
		ok, _, _ := seamlessGetRect.Call(root, uintptr(unsafe.Pointer(&rect)))
		return ok != 0 && rect == s.windows[i].rect
	}
	return false
}

func (p *nativeProjection) placeLocked(state *nativeProjectedWindow, tile nativeTile, rect seamlessRect) error {
	if !nativeIdentityMatches(state) {
		return errors.New("window identity changed")
	}
	if hung, _, _ := seamlessIsHung.Call(state.window.handle); hung != 0 {
		return errors.New("window is unresponsive")
	}
	var current seamlessRect
	if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&current))); ok == 0 {
		return errors.New("window rectangle unavailable")
	}
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	if state.applied {
		changed := current != state.lastRect || (shown != 0) != state.lastVisible
		if state.requestedShown == tile.Visible && (!tile.Visible || state.requestedRect == rect) {
			// Never fight an app's own resize or fullscreen transition on a
			// heartbeat. A hidden guest window is the one exception: rehide it
			// if the app chose to show itself while Omarchy is locked.
			if tile.Visible && !changed && nativeQemuAbove(state.window.handle) {
				const zFlags = 0x0010 | 0x0200 | 0x0001 | 0x0002 // NOACTIVATE | NOOWNERZORDER | NOSIZE | NOMOVE
				if ok, _, _ := procSetWindowPos.Call(state.window.handle, 0, 0, 0, 0, 0, zFlags); ok == 0 {
					return errors.New("window z order failed")
				}
			}
			if tile.Visible || shown == 0 {
				return nil
			}
		}
		if changed {
			var placement windowPlacementStruct
			placement.length = uint32(unsafe.Sizeof(placement))
			if ok, _, _ := procGetWindowPlacement.Call(state.window.handle, uintptr(unsafe.Pointer(&placement))); ok != 0 {
				state.restorePlacement = placement
				state.restoreVisible = shown != 0
			}
		}
	}
	firstMutation := !state.applied
	// From here onward the target may have changed even if a later Win32
	// call fails. Keep the snapshot for verified restoration.
	state.lastRect, state.lastVisible, state.applied = current, shown != 0, true
	if tile.Visible {
		if rect.right <= rect.left || rect.bottom <= rect.top {
			return errors.New("scaled tile is empty")
		}
		if state.original.showCmd == swShowMaximized && firstMutation {
			procShowWindow.Call(state.window.handle, 4) /* SW_SHOWNOACTIVATE */
		}
		const flags = 0x0010 | 0x0200 | 0x0040 // NOACTIVATE | NOOWNERZORDER | SHOWWINDOW
		if ok, _, _ := procSetWindowPos.Call(state.window.handle, 0, uintptr(rect.left), uintptr(rect.top),
			uintptr(rect.right-rect.left), uintptr(rect.bottom-rect.top), flags); ok == 0 {
			nativeRecordAppliedState(state)
			return errors.New("window placement failed")
		}
	} else {
		const flags = 0x0010 | 0x0200 | 0x0080 | 0x0001 | 0x0002 | 0x0004 // NOACTIVATE | NOOWNERZORDER | HIDEWINDOW | NOSIZE | NOMOVE | NOZORDER
		if ok, _, _ := procSetWindowPos.Call(state.window.handle, 0, 0, 0, 0, 0, flags); ok == 0 {
			nativeRecordAppliedState(state)
			return errors.New("window hide failed")
		}
	}
	if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&state.lastRect))); ok == 0 {
		state.uncertainMutation = true
		return errors.New("window result unavailable")
	}
	state.uncertainMutation = false
	shown, _, _ = procIsWindowVisible.Call(state.window.handle)
	state.lastVisible = shown != 0
	state.requestedRect = rect
	state.requestedShown = tile.Visible
	state.applied = true
	if state.lastVisible != tile.Visible || (tile.Visible && state.lastRect != rect) {
		return errors.New("window ignored requested placement")
	}
	return nil
}

func nativeRecordAppliedState(state *nativeProjectedWindow) {
	state.applied = true
	if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&state.lastRect))); ok == 0 {
		state.uncertainMutation = true
		return
	}
	state.uncertainMutation = false
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	state.lastVisible = shown != 0
	state.applied = true
}

// A false result retains the snapshot for a later retry. Destroyed/reused
// HWNDs and user-changed HWNDs are deliberately dropped without mutation.
func (p *nativeProjection) releaseLocked(state *nativeProjectedWindow) bool {
	if !nativeIdentityMatches(state) {
		nativeFreeRegion(state)
		return true
	}
	if !nativeRestoreRegion(state) {
		return false
	}
	if !state.applied {
		return true
	}
	var rect seamlessRect
	if ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return false
	}
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	// A user or app changed the window since projection. Leave that choice alone.
	compareRect, compareShown := state.lastRect, state.lastVisible
	if state.restoreStarted {
		compareRect, compareShown = state.restoreRect, state.restoreShown
	}
	if !state.uncertainMutation && (rect != compareRect || (shown != 0) != compareShown) {
		return true
	}
	if hung, _, _ := seamlessIsHung.Call(state.window.handle); hung != 0 {
		return false
	}
	placement := state.restorePlacement
	placement.length = uint32(unsafe.Sizeof(placement))
	if ok, _, _ := procSetWindowPlacement.Call(state.window.handle, uintptr(unsafe.Pointer(&placement))); ok == 0 {
		return false
	}
	if !state.restoreVisible {
		procShowWindow.Call(state.window.handle, 0) /* SW_HIDE */
	}
	shown, _, _ = procIsWindowVisible.Call(state.window.handle)
	state.restoreStarted = true
	state.uncertainMutation = false
	state.restoreShown = shown != 0
	seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&state.restoreRect)))
	if (shown != 0) != state.restoreVisible {
		return false
	}
	var actual windowPlacementStruct
	actual.length = uint32(unsafe.Sizeof(actual))
	if ok, _, _ := procGetWindowPlacement.Call(state.window.handle, uintptr(unsafe.Pointer(&actual))); ok == 0 {
		return false
	}
	if state.restoreVisible && (actual.showCmd != placement.showCmd || actual.normalPosition != placement.normalPosition) {
		return false
	}
	return true
}

func (p *nativeProjection) markAllRestoreLocked() {
	for _, state := range p.tracked {
		state.pendingRestore = true
		state.restoreAttempts = 0
		state.nextRestore = time.Time{}
	}
}

func (p *nativeProjection) retryRestoreLocked() {
	retryNativeRestores(p.tracked, p.releaseLocked)
}

func retryNativeRestores(tracked map[seamlessWindowKey]*nativeProjectedWindow, release func(*nativeProjectedWindow) bool) {
	retryNativeRestoresAt(tracked, release, time.Now())
}

func retryNativeRestoresAt(tracked map[seamlessWindowKey]*nativeProjectedWindow, release func(*nativeProjectedWindow) bool, now time.Time) {
	for key, state := range tracked {
		if !state.pendingRestore || now.Before(state.nextRestore) {
			continue
		}
		if release(state) {
			delete(tracked, key)
			continue
		}
		state.restoreAttempts++
		if state.restoreAttempts == 3 {
			logf("native window restore is retrying with backoff (PID %d)", key.pid)
		}
		if state.restoreAttempts >= 3 {
			shift := state.restoreAttempts - 3
			if shift > 4 {
				shift = 4
			}
			state.nextRestore = now.Add(time.Duration(1<<shift) * time.Second)
		}
	}
}
