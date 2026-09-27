//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"
)

const nativeForegroundQMPCommand = "__omarchy_native-foreground-handoff"
const nativeHandoffCooldown = 5 * time.Second
const nativeHandoffBudget = 250 * time.Millisecond

var nativeGetTickCount64 = kernel32.NewProc("GetTickCount64")

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
	HWND        uint64 `json:"hwnd"`
	PID         uint32 `json:"pid"`
	Created     uint64 `json:"created"`
	Property    string `json:"property"`
	Incarnation uint64 `json:"incarnation"`
	Expires     uint64 `json:"expires"`
}

type nativeQMPCaller interface {
	Call(context.Context, string, any, any) error
}

func nativeHandoffLeaseReady(committed, applyingUntil, now time.Time) bool {
	return !committed.IsZero() && now.Before(committed) &&
		!applyingUntil.IsZero() && now.Before(applyingUntil)
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
func (p *nativeProjection) nativeHandoffAllowedLocked(state *nativeProjectedWindow, rect seamlessRect) bool {
	if !p.experimentalForeground || p.closed || !guestUp.Load() || !nativeHandoffGuestReady.Load() ||
		!nativeHandoffLeaseReady(p.deadline, p.handoffUntil, time.Now()) ||
		state == nil || state.pendingRestore || !nativeIdentityMatches(state) ||
		state.window.incarnation == 0 || state.created == 0 || state.window.grantProperty == "" ||
		state.window.grantProperty != p.bridge.grantPropertyName() ||
		nativeWindowProperty(state.window.handle, state.window.grantProperty) != state.window.incarnation ||
		rect.right <= rect.left || rect.bottom <= rect.top {
		return false
	}
	key := seamlessKey(state.window)
	p.bridge.mu.Lock()
	grant, granted := p.bridge.grants[key]
	p.bridge.mu.Unlock()
	if !granted || !p.bridge.grantMatches(state.window, grant) {
		return false
	}
	qemu := qemuHwnd.Load()
	if qemu == 0 || nativeReadForegroundRelation().root != qemu {
		return false
	}
	if _, err := nativeQemuClientRect(); err != nil {
		return false
	}
	shown, _, _ := procIsWindowVisible.Call(state.window.handle)
	if shown == 0 {
		return false
	}
	var actual seamlessRect
	ok, _, _ := seamlessGetRect.Call(state.window.handle, uintptr(unsafe.Pointer(&actual)))
	return ok != 0 && actual == rect
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

// First try the ordinary host placement. An experiment opts into one bounded
// permission request only when QEMU itself is the foreground SDL window and
// every current grant check still passes. A failed attempt is not replayed on
// each layout heartbeat; an actual hide/show transition is cooldown limited.
func (p *nativeProjection) raiseAboveQemuLocked(state *nativeProjectedWindow, rect seamlessRect) error {
	originalError := nativeRaiseAboveQemu(state.window.handle)
	if originalError == nil || !p.nativeHandoffAllowedLocked(state, rect) {
		return originalError
	}
	key := seamlessKey(state.window)
	fingerprint := nativeHandoffFingerprint{key: key, incarnation: state.window.incarnation,
		rect: rect, qemuPID: qemuPid.Load(), qemuHWND: qemuHwnd.Load()}
	now := time.Now()
	if !nativeHandoffAttemptAllowed(p.handoffAttempts[key], fingerprint, now) {
		return originalError
	}
	p.handoffAttempts[key] = nativeHandoffAttempt{fingerprint: fingerprint, when: now}
	request := nativeHandoffRequest{
		HWND: uint64(state.window.handle), PID: state.window.PID,
		Created: state.created, Property: state.window.grantProperty,
		Incarnation: uint64(state.window.incarnation),
	}
	end := nativeHandoffEffectiveDeadline(p.deadline, p.handoffUntil)
	ctx, cancel := context.WithDeadline(context.Background(), end)
	defer cancel()
	if err := nativeForegroundHandoff(ctx, request); err != nil {
		logf("experimental native foreground permission unavailable: %v", err)
		return originalError
	}
	if ctx.Err() != nil || !p.nativeHandoffAllowedLocked(state, rect) {
		return errors.New("native foreground handoff identity changed")
	}
	return nativeRaiseAboveQemu(state.window.handle)
}
