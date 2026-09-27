package main

import (
	"math"
	"time"
)

const (
	adaptiveCPUInterval      = 2 * time.Second
	adaptiveCPUPressureDelay = 6 * time.Second
	adaptiveCPURecoveryDelay = 10 * time.Second
	adaptiveCPUBusyThreshold = 0.70
	adaptiveCPUIdleThreshold = 0.50
)

type adaptiveCPUSample struct {
	busy                      float64
	cpuKnown, foregroundKnown bool
	omarchyForeground         bool
}

// This policy adjusts scheduling preference, not a CPU quota. A background
// guest can still consume idle CPU time. Foreground Omarchy always gets its
// normal priority, and hysteresis avoids priority changes on brief load spikes.
type adaptiveCPUPolicy struct {
	yielding                  bool
	pressureSince, clearSince time.Time
}

func (p *adaptiveCPUPolicy) next(now time.Time, s adaptiveCPUSample) bool {
	if now.IsZero() || !s.cpuKnown || !s.foregroundKnown || s.omarchyForeground ||
		math.IsNaN(s.busy) || math.IsInf(s.busy, 0) || s.busy < 0 || s.busy > 1 {
		*p = adaptiveCPUPolicy{}
		return false
	}
	if !p.yielding {
		if s.busy < adaptiveCPUBusyThreshold {
			p.pressureSince = time.Time{}
			return false
		}
		if p.pressureSince.IsZero() || now.Before(p.pressureSince) {
			p.pressureSince = now
		}
		if now.Sub(p.pressureSince) >= adaptiveCPUPressureDelay {
			p.yielding = true
			p.pressureSince = time.Time{}
		}
		return p.yielding
	}
	if s.busy > adaptiveCPUIdleThreshold {
		p.clearSince = time.Time{}
		return true
	}
	if p.clearSince.IsZero() || now.Before(p.clearSince) {
		p.clearSince = now
	}
	if now.Sub(p.clearSince) >= adaptiveCPURecoveryDelay {
		*p = adaptiveCPUPolicy{}
	}
	return p.yielding
}
