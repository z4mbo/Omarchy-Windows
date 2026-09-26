package main

import (
	"context"
	"fmt"
	"time"
)

// This controller is intentionally not called by the normal launcher. The
// Windows entry point verifies an exact experimental QEMU build before it can
// be used. QEMU's ordinary Windows runtime cannot reclaim ballooned pages.
const (
	balloonReclaimCapabilityProperty = "x-omarchy-balloon-reclaim-active"
	balloonSampleInterval            = 5 * time.Second
	balloonGuestStatsMaxAge          = 20 * time.Second
	balloonPendingTimeout            = 45 * time.Second
	balloonGrowStableFor             = 90 * time.Second
	balloonGrowCooldown              = 120 * time.Second
	balloonStepMiB                   = 256
	balloonMaxShrinkMiB              = 1024
	balloonMaxGrowMiB                = 512
	balloonGuestReserveMiB           = 1536
	balloonGrowHysteresisMiB         = 2048
	balloonShrinkMarginMiB           = 512
)

type experimentalBalloonPolicy struct {
	bootMiB, floorMiB, headroomMiB       int
	lastCommand, highSince, pendingSince time.Time
	pendingTargetMiB                     int
}

func newExperimentalBalloonPolicy(bootMiB, floorMiB, hostTotalMiB int) (*experimentalBalloonPolicy, error) {
	if bootMiB < 2048 || bootMiB > maximumGuestMemoryMiB || floorMiB < 2048 || floorMiB > bootMiB ||
		bootMiB%balloonStepMiB != 0 || floorMiB%balloonStepMiB != 0 || hostTotalMiB < bootMiB+2048 {
		return nil, fmt.Errorf("invalid experimental balloon memory limits")
	}
	// Keep more room for Windows as machine size grows, with a ceiling so a
	// modest PC can still run the guest. This is physical available RAM, not
	// QEMU's working-set target.
	headroom := max(3072, min(8192, hostTotalMiB/8))
	return &experimentalBalloonPolicy{bootMiB: bootMiB, floorMiB: floorMiB, headroomMiB: headroom}, nil
}

type experimentalBalloonSample struct {
	hostAvailableMiB, guestActualMiB, guestAvailableMiB int
	guestStatsFresh                                     bool
}

// nextTarget returns a logical guest RAM target. A QMP balloon command is only
// a request, so a pending request must be observed before another is issued.
// Missing or stale guest statistics never authorize shrinking the guest.
func (p *experimentalBalloonPolicy) nextTarget(now time.Time, s experimentalBalloonSample) (int, error) {
	if now.IsZero() || s.hostAvailableMiB < 0 || s.guestActualMiB < p.floorMiB ||
		s.guestActualMiB > p.bootMiB {
		return 0, fmt.Errorf("invalid experimental balloon sample")
	}
	if p.pendingTargetMiB != 0 {
		if balloonAbs(s.guestActualMiB-p.pendingTargetMiB) <= balloonStepMiB/4 {
			p.pendingTargetMiB = 0
			p.pendingSince = time.Time{}
		} else if now.Sub(p.pendingSince) >= balloonPendingTimeout {
			return 0, fmt.Errorf("guest did not reach experimental balloon target")
		} else {
			return 0, nil
		}
	}
	if s.hostAvailableMiB < p.headroomMiB {
		p.highSince = time.Time{}
		if !s.guestStatsFresh || s.guestAvailableMiB <= balloonGuestReserveMiB {
			return 0, nil
		}
		deficit := p.headroomMiB - s.hostAvailableMiB + balloonShrinkMarginMiB
		delta := min(balloonMaxShrinkMiB, roundUpBalloonMiB(deficit))
		delta = min(delta, s.guestActualMiB-p.floorMiB)
		delta = min(delta, roundDownBalloonMiB(s.guestAvailableMiB-balloonGuestReserveMiB))
		if delta < balloonStepMiB {
			return 0, nil
		}
		return p.request(now, s.guestActualMiB-delta), nil
	}
	if s.hostAvailableMiB < p.headroomMiB+balloonGrowHysteresisMiB || s.guestActualMiB == p.bootMiB {
		p.highSince = time.Time{}
		return 0, nil
	}
	if p.highSince.IsZero() {
		p.highSince = now
		return 0, nil
	}
	if now.Sub(p.highSince) < balloonGrowStableFor ||
		(!p.lastCommand.IsZero() && now.Sub(p.lastCommand) < balloonGrowCooldown) {
		return 0, nil
	}
	delta := min(balloonMaxGrowMiB, p.bootMiB-s.guestActualMiB)
	delta = min(delta, roundDownBalloonMiB(s.hostAvailableMiB-p.headroomMiB-balloonShrinkMarginMiB))
	if delta < balloonStepMiB {
		return 0, nil
	}
	p.highSince = time.Time{}
	return p.request(now, s.guestActualMiB+delta), nil
}

func (p *experimentalBalloonPolicy) request(now time.Time, targetMiB int) int {
	p.pendingTargetMiB = targetMiB
	p.pendingSince = now
	p.lastCommand = now
	return targetMiB
}

func roundDownBalloonMiB(n int) int { return n / balloonStepMiB * balloonStepMiB }
func roundUpBalloonMiB(n int) int   { return (n + balloonStepMiB - 1) / balloonStepMiB * balloonStepMiB }
func balloonAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

type experimentalBalloonQMP interface {
	Call(context.Context, string, any, any) error
}

type experimentalBalloonController struct {
	policy              *experimentalBalloonPolicy
	qmp                 experimentalBalloonQMP
	balloonPath         string
	measureAvailableMiB func() (int, error)
	stillRunning        func() bool
	now                 func() time.Time
}

func (c *experimentalBalloonController) ensureReclaimActive(ctx context.Context) error {
	var active bool
	// virtio-balloon-pci forwards its standard statistics properties, but QOM
	// keeps this experimental property on the virtio-balloon-device child.
	if err := c.qmp.Call(ctx, "qom-get", map[string]any{
		"path": c.balloonPath + "/virtio-backend", "property": balloonReclaimCapabilityProperty,
	}, &active); err != nil {
		return fmt.Errorf("experimental QEMU cannot attest RAM reclaim: %w", err)
	}
	if !active {
		return fmt.Errorf("experimental QEMU RAM reclaim is not active")
	}
	return nil
}

func (c *experimentalBalloonController) tick(ctx context.Context) error {
	if !c.stillRunning() {
		return fmt.Errorf("experimental QEMU process exited")
	}
	if err := c.ensureReclaimActive(ctx); err != nil {
		return err
	}
	var state vmRuntimeStatus
	if err := c.qmp.Call(ctx, "query-status", nil, &state); err != nil {
		return err
	}
	if !state.Running || state.Status != "running" {
		return fmt.Errorf("experimental guest is not running")
	}
	var balloon struct {
		Actual int64 `json:"actual"`
	}
	if err := c.qmp.Call(ctx, "query-balloon", nil, &balloon); err != nil {
		return err
	}
	if balloon.Actual <= 0 || balloon.Actual%(1<<20) > 64<<10 {
		return fmt.Errorf("invalid QMP balloon actual size")
	}
	var stats struct {
		Stats      map[string]int64 `json:"stats"`
		LastUpdate int64            `json:"last-update"`
	}
	if err := c.qmp.Call(ctx, "qom-get", map[string]any{"path": c.balloonPath, "property": "guest-stats"}, &stats); err != nil {
		return err
	}
	hostAvailable, err := c.measureAvailableMiB()
	if err != nil {
		return fmt.Errorf("Windows available memory is unavailable: %w", err)
	}
	if hostAvailable <= 0 {
		return fmt.Errorf("Windows available memory is unavailable")
	}
	now := c.now()
	guestAvailable := stats.Stats["stat-available-memory"]
	statsAge := now.Sub(time.Unix(stats.LastUpdate, 0))
	sample := experimentalBalloonSample{
		hostAvailableMiB:  hostAvailable,
		guestActualMiB:    int(balloon.Actual >> 20),
		guestAvailableMiB: int(guestAvailable >> 20),
		guestStatsFresh:   stats.LastUpdate > 0 && statsAge >= 0 && statsAge <= balloonGuestStatsMaxAge && guestAvailable >= 0,
	}
	target, err := c.policy.nextTarget(now, sample)
	if err != nil || target == 0 {
		return err
	}
	return c.qmp.Call(ctx, "balloon", map[string]any{"value": int64(target) << 20}, nil)
}

// run does not start on its own. Only the explicitly called, hash-gated
// Windows adapter may construct it for the experimental QEMU executable.
func (c *experimentalBalloonController) run(ctx context.Context) error {
	if c == nil || c.policy == nil || c.qmp == nil || c.balloonPath == "" ||
		c.measureAvailableMiB == nil || c.stillRunning == nil || c.now == nil {
		return fmt.Errorf("experimental balloon controller is incomplete")
	}
	if err := c.ensureReclaimActive(ctx); err != nil {
		return err
	}
	if err := c.qmp.Call(ctx, "qom-set", map[string]any{"path": c.balloonPath,
		"property": "guest-stats-polling-interval", "value": 5}, nil); err != nil {
		return err
	}
	ticker := time.NewTicker(balloonSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := c.tick(ctx); err != nil {
				return err
			}
		}
	}
}
