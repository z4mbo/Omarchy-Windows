//go:build windows

package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeNativeHandoffQMP struct {
	supported bool
	calls     []string
	request   nativeHandoffRequest
	err       error
}

func (f *fakeNativeHandoffQMP) Call(_ context.Context, command string, arguments any, result any) error {
	f.calls = append(f.calls, command)
	if command == "query-commands" {
		commands := `[{"name":"query-status"}]`
		if f.supported {
			commands = `[{"name":"query-status"},{"name":"__omarchy_native-foreground-handoff"}]`
		}
		return json.Unmarshal([]byte(commands), result)
	}
	if command != nativeForegroundQMPCommand {
		return errors.New("unexpected QMP command")
	}
	f.request = arguments.(nativeHandoffRequest)
	return f.err
}

func TestNativeHandoffOnlyAfterFeatureDiscovery(t *testing.T) {
	request := nativeHandoffRequest{HWND: 0xabc, PID: 301, Created: 918, Property: "Omarchy.Windows.Grant.test", Incarnation: 755}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	missing := &fakeNativeHandoffQMP{}
	if err := nativeIssueForegroundHandoff(ctx, missing, request); err == nil || len(missing.calls) != 1 {
		t.Fatal("unsupported runtime received the privileged command")
	}
	supported := &fakeNativeHandoffQMP{supported: true}
	if err := nativeIssueForegroundHandoff(ctx, supported, request); err != nil {
		t.Fatal(err)
	}
	if len(supported.calls) != 2 || supported.calls[1] != nativeForegroundQMPCommand ||
		supported.request.HWND != request.HWND || supported.request.PID != request.PID ||
		supported.request.Created != request.Created || supported.request.Property != request.Property ||
		supported.request.Incarnation != request.Incarnation || supported.request.Expires == 0 {
		t.Fatal("feature-supported runtime did not receive the exact grant identity")
	}
}

func TestNativeHandoffExpiryNeverOutlivesApplyBudget(t *testing.T) {
	deadline := time.Now().Add(2 * time.Second)
	now := deadline.Add(-40 * time.Millisecond)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	expires, err := nativeHandoffExpires(ctx, 1000, now)
	if err != nil || expires < 1001 || expires > 1040 {
		t.Fatalf("deadline not bounded to current Apply: expiry=%d err=%v", expires, err)
	}
	longNow := time.Now()
	long, cancelLong := context.WithDeadline(context.Background(), longNow.Add(time.Second))
	defer cancelLong()
	expires, err = nativeHandoffExpires(long, 1000, longNow)
	if err != nil || expires != 1200 {
		t.Fatalf("QEMU expiry exceeded 200 ms cap: expiry=%d err=%v", expires, err)
	}
	expired, cancelExpired := context.WithDeadline(context.Background(), longNow.Add(-time.Millisecond))
	defer cancelExpired()
	if _, err := nativeHandoffExpires(expired, 1000, longNow); err == nil {
		t.Fatal("expired layout minted a QMP permission deadline")
	}
}

func TestNativeHandoffAttemptRequiresTransitionAndCooldown(t *testing.T) {
	base := time.Now()
	fingerprint := nativeHandoffFingerprint{key: seamlessWindowKey{pid: 301, handle: 0xabc},
		incarnation: 755, rect: seamlessRect{1, 2, 101, 202}, qemuPID: 55, qemuHWND: 0x555}
	if !nativeHandoffAttemptAllowed(nativeHandoffAttempt{}, fingerprint, base) {
		t.Fatal("initial attempt denied")
	}
	previous := nativeHandoffAttempt{fingerprint: fingerprint, when: base}
	if nativeHandoffAttemptAllowed(previous, fingerprint, base.Add(time.Hour)) {
		t.Fatal("same visible tile replayed a handoff")
	}
	changed := fingerprint
	changed.rect.right++
	if nativeHandoffAttemptAllowed(previous, changed, base.Add(time.Second)) {
		t.Fatal("fast layout oscillation bypassed cooldown")
	}
	if !nativeHandoffAttemptAllowed(previous, changed, base.Add(nativeHandoffCooldown)) {
		t.Fatal("changed tile remained permanently blocked")
	}
	previous.hidden = true
	if nativeHandoffAttemptAllowed(previous, fingerprint, base.Add(time.Second)) ||
		!nativeHandoffAttemptAllowed(previous, fingerprint, base.Add(nativeHandoffCooldown)) {
		t.Fatal("hide/show transition did not honor cooldown")
	}
}

func TestNativeHandoffRequiresCommittedAndCurrentLease(t *testing.T) {
	now := time.Now()
	if nativeHandoffLeaseReady(time.Time{}, now.Add(time.Second), now) ||
		nativeHandoffLeaseReady(now.Add(time.Second), time.Time{}, now) ||
		nativeHandoffLeaseReady(now.Add(-time.Second), now.Add(time.Second), now) ||
		nativeHandoffLeaseReady(now.Add(time.Second), now.Add(-time.Second), now) {
		t.Fatal("missing or expired native layout lease permitted handoff")
	}
	if !nativeHandoffLeaseReady(now.Add(time.Second), now.Add(250*time.Millisecond), now) {
		t.Fatal("valid committed layout and current request were rejected")
	}
	if got := nativeHandoffEffectiveDeadline(now.Add(50*time.Millisecond), now.Add(250*time.Millisecond)); got != now.Add(50*time.Millisecond) {
		t.Fatal("permission could outlive the committed lease")
	}
	if got := nativeHandoffEffectiveDeadline(now.Add(time.Second), now.Add(250*time.Millisecond)); got != now.Add(250*time.Millisecond) {
		t.Fatal("permission could outlive the current Apply budget")
	}
}
