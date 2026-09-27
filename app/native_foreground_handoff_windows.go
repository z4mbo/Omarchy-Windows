//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
	"unsafe"
)

const nativeForegroundQMPCommand = "__omarchy_native-foreground-handoff-v2"
const nativeHandoffCooldown = 5 * time.Second
const nativeHandoffBudget = 250 * time.Millisecond

var nativeGetTickCount64 = kernel32.NewProc("GetTickCount64")
var nativeSendMessageTimeoutW = user32.NewProc("SendMessageTimeoutW")

var (
	errNativeActivationStillBehind = errors.New("window remained behind QEMU after delegated activation")
	errNativeActivationNoFocus     = errors.New("native window did not become foreground after activation")
	errNativeActivationDenied      = errors.New("Windows denied launcher foreground activation")
	errNativeActivationTimeout     = errors.New("native foreground processing did not finish within the budget")
)

type nativeHandoffFingerprint struct {
	key         seamlessWindowKey
	incarnation uintptr
	rect        seamlessRect
	qemuPID     uint32
	qemuHWND    uintptr
}

type nativeHandoffAttempt struct {
	fingerprint nativeHandoffFingerprint
	when        time.Time
	hidden      bool
}

type nativeHandoffRequest struct {
	HWND            uint64 `json:"hwnd"`
	PID             uint32 `json:"pid"`
	Created         uint64 `json:"created"`
	Property        string `json:"property"`
	Incarnation     uint64 `json:"incarnation"`
	Expires         uint64 `json:"expires"`
	LauncherPID     uint32 `json:"launcher-pid"`
	LauncherCreated uint64 `json:"launcher-created"`
	LauncherHWND    uint64 `json:"launcher-hwnd"`
}

type nativeQMPCaller interface {
	Call(context.Context, string, any, any) error
}

func nativeHandoffLeaseReady(committed, applyingUntil, now time.Time) bool {
	return !committed.IsZero() && now.Before(committed) &&
		!applyingUntil.IsZero() && now.Before(applyingUntil)
}

func nativeHandoffVisibleTiles(tiles []nativeTile) int {
	count := 0
	for _, tile := range tiles {
		if tile.Visible {
			count++
		}
	}
	return count
}

func nativeHandoffEffectiveDeadline(committed, applyingUntil time.Time) time.Time {
	if committed.Before(applyingUntil) {
		return committed
	}
	return applyingUntil
}

func nativeHandoffExpires(ctx context.Context, uptimeMillis uint64, now time.Time) (uint64, error) {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil {
		return 0, errors.New("native foreground request has no live deadline")
	}
	remaining := deadline.Sub(now).Milliseconds()
	if remaining < 1 {
		return 0, errors.New("native foreground request deadline elapsed")
	}
	if remaining > 200 {
		remaining = 200
	}
	if uptimeMillis > ^uint64(0)-uint64(remaining) {
		return 0, errors.New("Windows uptime overflow")
	}
	return uptimeMillis + uint64(remaining), nil
}

func nativeWindowsUptimeMillis() uint64 {
	value, _, _ := nativeGetTickCount64.Call()
	return uint64(value)
}

func nativeLauncherHandoffIdentity() (uint32, uint64, uintptr, error) {
	pid := uint32(os.Getpid())
	hwnd := trayWindow.Load()
	if pid == 0 || hwnd == 0 {
		return 0, 0, 0, errors.New("launcher tray is unavailable")
	}
	var owner uint32
	thread, _, _ := procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	if thread == 0 || owner != pid {
		return 0, 0, 0, errors.New("launcher tray identity changed")
	}
	created, err := nativeProcessCreated(pid)
	if err != nil || created == 0 {
		return 0, 0, 0, errors.New("launcher process identity is unavailable")
	}
	return pid, created, hwnd, nil
}

func nativeHandoffAttemptAllowed(previous nativeHandoffAttempt, fingerprint nativeHandoffFingerprint, now time.Time) bool {
	if previous.when.IsZero() {
		return true
	}
	if previous.fingerprint == fingerprint && !previous.hidden {
		return false
	}
	return !now.Before(previous.when.Add(nativeHandoffCooldown))
}

func (p *nativeProjection) noteHandoffVisibilityLocked(key seamlessWindowKey, visible bool) {
	if !visible {
		if previous, ok := p.handoffAttempts[key]; ok {
			previous.hidden = true
			p.handoffAttempts[key] = previous
		}
	}
}

func (p *nativeProjection) markAllHandoffsHiddenLocked() {
	for key := range p.handoffAttempts {
		p.noteHandoffVisibilityLocked(key, false)
	}
}

// The caller holds p.mu and the physical DPI context. The active layout was
// validated in Apply; repeat the live host grant, HWND and foreground checks
// immediately around the short QMP operation because they can change meanwhile.
func (p *nativeProjection) nativeHandoffAllowedLocked(state *nativeProjectedWindow, rect seamlessRect) (bool, string) {
	if !p.experimentalForeground || p.closed {
		return false, "disabled_or_closed"
	}
	if !guestUp.Load() || !nativeHandoffGuestReady.Load() {
		return false, "guest_not_ready"
	}
	now := time.Now()
	if !nativeHandoffLeaseReady(p.deadline, p.handoffUntil, now) {
		if p.deadline.IsZero() || !now.Before(p.deadline) {
			return false, "committed_lease_expired"
		}
		return false, "apply_budget_expired"
	}
	if p.handoffVisibleCount != 1 {
		return false, "ambiguous_visible_tiles"
	}
	if state == nil || state.pendingRestore || !nativeIdentityMatches(state) {
		return false, "window_identity_unavailable"
	}
	if state.window.incarnation == 0 || state.created == 0 || state.window.grantProperty == "" ||
		state.window.grantProperty != p.bridge.grantPropertyName() ||
		nativeWindowProperty(state.window.handle, state.window.grantProperty) != state.window.incarnation {
		return false, "grant_marker_changed"
	}
	if rect.right <= rect.left || rect.bottom <= rect.top {
		return false, "invalid_tile"
	}
	key := seamlessKey(state.window)
	p.bridge.mu.Lock()
	grant, granted := p.bridge.grants[key]
	p.bridge.mu.Unlock()
	if !granted || !p.bridge.grantMatches(state.window, grant) {
		return false, "grant_changed"
	}
	qemu := qemuHwnd.Load()
	if qemu == 0 || nativeReadForegroundRelation().root != qemu {
		return false, "qemu_not_foreground"
	}
	if _, err := nativeQemuClientRect(); err != nil {
		return false, "qemu_display_unavailable"
	}
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	if shown == 0 {
		return false, "native_window_hidden"
	}
	var actual seamlessRect
	ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&actual)))
	if ok == 0 || actual != rect {
		return false, "native_tile_mismatch"
	}
	return true, "eligible"
}

func (p *nativeProjection) logHandoffOutcomeLocked(state *nativeProjectedWindow, reason string) {
	if state == nil || !p.handoffDiagnostics.allow(nativeFullscreenDiagnosticIdentity(state), reason, time.Now()) {
		return
	}
	// No title, grant property, bearer, native HWND or request body is logged.
	logf("experimental native foreground handoff: pid=%d reason=%s", state.window.PID, reason)
}

func nativeIssueForegroundHandoff(ctx context.Context, caller nativeQMPCaller, request nativeHandoffRequest) error {
	var commands []struct {
		Name string `json:"name"`
	}
	if err := caller.Call(ctx, "query-commands", nil, &commands); err != nil {
		return fmt.Errorf("cannot verify experimental QMP command: %w", err)
	}
	supported := false
	for _, command := range commands {
		if command.Name == nativeForegroundQMPCommand {
			supported = true
			break
		}
	}
	if !supported {
		return errors.New("the selected runtime does not support native foreground handoff")
	}
	expires, err := nativeHandoffExpires(ctx, nativeWindowsUptimeMillis(), time.Now())
	if err != nil {
		return err
	}
	request.Expires = expires
	return caller.Call(ctx, nativeForegroundQMPCommand, request, nil)
}

func nativeForegroundHandoff(ctx context.Context, request nativeHandoffRequest) error {
	path, err := qmpControlPath(qmpNativePort)
	if err != nil {
		return err
	}
	if err := verifyQMPControlSocketACL(path); err != nil {
		return fmt.Errorf("private native QMP socket verification failed: %w", err)
	}
	client, err := dialQMPControl(ctx, qmpNativePort)
	if err != nil {
		return err
	}
	defer client.Close()
	return nativeIssueForegroundHandoff(ctx, client, request)
}

// QEMU grants its bound launcher one foreground attempt. Activation can be
// asynchronous across input queues, so process a bounded no-op on the exact
// target thread before checking the result. This never repeats activation.
func nativeActivateGrantedWindowAboveQemu(ctx context.Context, state *nativeProjectedWindow) error {
	hwnd := state.window.handle
	qemu := qemuHwnd.Load()
	if qemu == 0 || nativeReadForegroundRelation().root != qemu {
		return errors.New("QEMU is no longer the foreground window")
	}
	if hung, _, _ := seamlessIsHung.Call(hwnd); hung != 0 {
		return errors.New("native window became unresponsive")
	}
	if ctx.Err() != nil || !nativeIdentityMatches(state) ||
		nativeWindowProperty(hwnd, state.window.grantProperty) != state.window.incarnation {
		return errors.New("native foreground identity or layout deadline changed")
	}
	if ok, _, _ := procSetForegroundWindow.Call(hwnd); ok == 0 {
		return errNativeActivationDenied
	}
	deadline, exists := ctx.Deadline()
	if !exists || ctx.Err() != nil {
		return errors.New("foreground activation budget expired")
	}
	remaining := time.Until(deadline).Milliseconds()
	if remaining < 1 {
		return errors.New("foreground activation budget expired")
	}
	if remaining > 100 {
		remaining = 100
	}
	var messageResult uintptr
	const wmNull = 0
	const smtoAbortIfHungBlock = 0x0001 | 0x0002
	if ok, _, _ := nativeSendMessageTimeoutW.Call(hwnd, wmNull, 0, 0,
		smtoAbortIfHungBlock, uintptr(remaining), uintptr(unsafe.Pointer(&messageResult))); ok == 0 {
		return errNativeActivationTimeout
	}
	if ctx.Err() != nil || !nativeIdentityMatches(state) ||
		nativeWindowProperty(hwnd, state.window.grantProperty) != state.window.incarnation {
		return errors.New("native foreground identity or layout deadline changed")
	}
	if nativeQemuAbove(hwnd) {
		nativeLogZOrderFailure("QEMU remained above after delegated activation", hwnd)
		return errNativeActivationStillBehind
	}
	foreground := nativeReadForegroundRelation()
	if !nativeForegroundBelongsToProjected(foreground, seamlessKey(state.window), true, nativeIdentityMatches(state)) {
		return errNativeActivationNoFocus
	}
	return nil
}

// First try the ordinary host placement. An experiment opts into one bounded
// permission request only when QEMU itself is the foreground SDL window and
// every current grant check still passes. A failed attempt is not replayed on
// each layout heartbeat; an actual hide/show transition is cooldown limited.
func (p *nativeProjection) raiseAboveQemuLocked(state *nativeProjectedWindow, rect seamlessRect) error {
	originalError := nativeRaiseAboveQemu(state.window.handle)
	if originalError == nil || !p.experimentalForeground {
		return originalError
	}
	if allowed, reason := p.nativeHandoffAllowedLocked(state, rect); !allowed {
		p.logHandoffOutcomeLocked(state, reason)
		return originalError
	}
	key := seamlessKey(state.window)
	fingerprint := nativeHandoffFingerprint{key: key, incarnation: state.window.incarnation,
		rect: rect, qemuPID: qemuPid.Load(), qemuHWND: qemuHwnd.Load()}
	now := time.Now()
	if !nativeHandoffAttemptAllowed(p.handoffAttempts[key], fingerprint, now) {
		p.logHandoffOutcomeLocked(state, "retry_suppressed")
		return originalError
	}
	p.handoffAttempts[key] = nativeHandoffAttempt{fingerprint: fingerprint, when: now}
	request := nativeHandoffRequest{
		HWND: uint64(state.window.handle), PID: state.window.PID,
		Created: state.created, Property: state.window.grantProperty,
		Incarnation: uint64(state.window.incarnation),
	}
	launcherPID, launcherCreated, launcherHWND, err := nativeLauncherHandoffIdentity()
	if err != nil {
		p.logHandoffOutcomeLocked(state, "launcher_identity_unavailable")
		return originalError
	}
	request.LauncherPID = launcherPID
	request.LauncherCreated = launcherCreated
	request.LauncherHWND = uint64(launcherHWND)
	end := nativeHandoffEffectiveDeadline(p.deadline, p.handoffUntil)
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	if err := nativeForegroundHandoff(ctx, request); err != nil {
		p.logHandoffOutcomeLocked(state, "qmp_permission_unavailable")
		logf("experimental native foreground permission unavailable: %v", err)
		return originalError
	}
	if ctx.Err() != nil {
		p.logHandoffOutcomeLocked(state, "reply_after_deadline")
		return errors.New("native foreground handoff identity changed")
	}
	if allowed, reason := p.nativeHandoffAllowedLocked(state, rect); !allowed {
		p.logHandoffOutcomeLocked(state, "post_permission_"+reason)
		return errors.New("native foreground handoff identity changed")
	}
	if err := nativeActivateGrantedWindowAboveQemu(ctx, state); err != nil {
		reason := "permission_accepted_activation_failed"
		if errors.Is(err, errNativeActivationStillBehind) {
			reason = "permission_accepted_still_behind"
		} else if errors.Is(err, errNativeActivationNoFocus) {
			reason = "permission_accepted_no_foreground"
		} else if errors.Is(err, errNativeActivationDenied) {
			reason = "permission_accepted_activation_denied"
		} else if errors.Is(err, errNativeActivationTimeout) {
			reason = "permission_accepted_processing_timeout"
		}
		p.logHandoffOutcomeLocked(state, reason)
		return err
	}
	p.logHandoffOutcomeLocked(state, "permission_accepted_placement_verified")
	return nil
}
