package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func TestExperimentalBalloonPolicyPressureAndRecovery(t *testing.T) {
	p, err := newExperimentalBalloonPolicy(4096, 2048, 65536)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1000, 0)
	s := experimentalBalloonSample{hostAvailableMiB: 6000, guestActualMiB: 4096,
		guestAvailableMiB: 3000, guestStatsFresh: true}
	if target, err := p.nextTarget(start, s); err != nil || target != 3072 {
		t.Fatalf("first shrink = %d, %v; want 3072", target, err)
	}
	if target, err := p.nextTarget(start.Add(5*time.Second), s); err != nil || target != 0 {
		t.Fatalf("second request while first pending = %d, %v", target, err)
	}
	s.guestActualMiB, s.hostAvailableMiB, s.guestAvailableMiB = 3072, 7100, 2600
	if target, err := p.nextTarget(start.Add(10*time.Second), s); err != nil || target != 2048 {
		t.Fatalf("second bounded shrink = %d, %v; want 2048", target, err)
	}
	s.guestActualMiB, s.hostAvailableMiB, s.guestAvailableMiB = 2048, 12000, 1500
	if target, err := p.nextTarget(start.Add(20*time.Second), s); err != nil || target != 0 {
		t.Fatalf("early growth = %d, %v", target, err)
	}
	if target, err := p.nextTarget(start.Add(100*time.Second), s); err != nil || target != 0 {
		t.Fatalf("growth before stable/cooldown = %d, %v", target, err)
	}
	if target, err := p.nextTarget(start.Add(145*time.Second), s); err != nil || target != 2560 {
		t.Fatalf("gradual recovery = %d, %v; want 2560", target, err)
	}
}

func TestExperimentalBalloonPolicyNeedsFreshGuestHeadroom(t *testing.T) {
	p, err := newExperimentalBalloonPolicy(4096, 2048, 65536)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(2000, 0)
	s := experimentalBalloonSample{hostAvailableMiB: 2000, guestActualMiB: 4096,
		guestAvailableMiB: 4096}
	if target, err := p.nextTarget(now, s); err != nil || target != 0 {
		t.Fatalf("stale guest statistics authorized shrink: %d, %v", target, err)
	}
	s.guestStatsFresh, s.guestAvailableMiB = true, 1700
	if target, err := p.nextTarget(now.Add(time.Second), s); err != nil || target != 0 {
		t.Fatalf("insufficient guest headroom authorized shrink: %d, %v", target, err)
	}
	s.guestAvailableMiB = 2300
	if target, err := p.nextTarget(now.Add(2*time.Second), s); err != nil || target != 3584 {
		t.Fatalf("headroom-limited shrink = %d, %v; want 3584", target, err)
	}
}

func TestExperimentalBalloonPolicyStopsOnUnfulfilledRequest(t *testing.T) {
	p, err := newExperimentalBalloonPolicy(4096, 2048, 65536)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(3000, 0)
	s := experimentalBalloonSample{hostAvailableMiB: 6000, guestActualMiB: 4096,
		guestAvailableMiB: 3000, guestStatsFresh: true}
	if target, err := p.nextTarget(now, s); err != nil || target == 0 {
		t.Fatalf("initial request = %d, %v", target, err)
	}
	if _, err := p.nextTarget(now.Add(balloonPendingTimeout), s); err == nil {
		t.Fatal("unfulfilled request did not stop the controller")
	}
}

func TestExperimentalBalloonPolicyGrowthNeedsUninterruptedHeadroom(t *testing.T) {
	p, err := newExperimentalBalloonPolicy(4096, 2048, 65536)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(5000, 0)
	s := experimentalBalloonSample{hostAvailableMiB: 12000, guestActualMiB: 2048}
	if target, err := p.nextTarget(start, s); err != nil || target != 0 {
		t.Fatalf("initial high sample = %d, %v", target, err)
	}
	s.hostAvailableMiB = 9000 // Between shrink and growth thresholds.
	if target, err := p.nextTarget(start.Add(80*time.Second), s); err != nil || target != 0 {
		t.Fatalf("middle-band sample = %d, %v", target, err)
	}
	s.hostAvailableMiB = 12000
	if target, err := p.nextTarget(start.Add(100*time.Second), s); err != nil || target != 0 {
		t.Fatalf("growth timer was not reset: %d, %v", target, err)
	}
	if target, err := p.nextTarget(start.Add(191*time.Second), s); err != nil || target != 2560 {
		t.Fatalf("growth after stable headroom = %d, %v", target, err)
	}
}

type fakeExperimentalBalloonQMP struct {
	commands       []string
	actual         int64
	guestAvailable int64
	updated        int64
	target         int64
}

func (f *fakeExperimentalBalloonQMP) Call(_ context.Context, command string, args any, result any) error {
	f.commands = append(f.commands, command)
	var response any
	switch command {
	case "query-status":
		response = map[string]any{"running": true, "status": "running"}
	case "query-balloon":
		response = map[string]any{"actual": f.actual}
	case "qom-get":
		response = map[string]any{"stats": map[string]int64{"stat-available-memory": f.guestAvailable}, "last-update": f.updated}
	case "balloon":
		arg, ok := args.(map[string]any)
		if !ok {
			return fmt.Errorf("invalid balloon arguments")
		}
		f.target, ok = arg["value"].(int64)
		if !ok {
			return fmt.Errorf("invalid balloon target")
		}
		return nil
	default:
		return fmt.Errorf("unexpected QMP command %q", command)
	}
	data, _ := json.Marshal(response)
	return json.Unmarshal(data, result)
}

func TestExperimentalBalloonTickUsesQMPAndHostSample(t *testing.T) {
	policy, err := newExperimentalBalloonPolicy(4096, 2048, 65536)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(4000, 0)
	qmp := &fakeExperimentalBalloonQMP{actual: 4096 << 20, guestAvailable: 3000 << 20, updated: now.Unix()}
	c := experimentalBalloonController{policy: policy, qmp: qmp,
		balloonPath:         "/machine/peripheral/experimental-balloon",
		measureAvailableMiB: func() (int, error) { return 6000, nil },
		stillRunning:        func() bool { return true }, now: func() time.Time { return now }}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if qmp.target != 3072<<20 {
		t.Fatalf("QMP target = %d, want %d", qmp.target, int64(3072)<<20)
	}
	if len(qmp.commands) != 4 || qmp.commands[0] != "query-status" ||
		qmp.commands[1] != "query-balloon" || qmp.commands[2] != "qom-get" || qmp.commands[3] != "balloon" {
		t.Fatalf("QMP sequence = %v", qmp.commands)
	}
	qmp.target = 0
	qmp.updated = now.Add(-balloonGuestStatsMaxAge - time.Second).Unix()
	policy.pendingTargetMiB = 0
	policy.pendingSince = time.Time{}
	if err := c.tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if qmp.target != 0 {
		t.Fatal("stale guest statistics caused another balloon command")
	}
}
