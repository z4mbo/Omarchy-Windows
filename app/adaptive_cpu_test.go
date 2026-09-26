package main

import (
	"math"
	"testing"
	"time"
)

func TestAdaptiveCPUYieldsOnlyAfterSustainedBackgroundPressure(t *testing.T) {
	now := time.Unix(1000, 0)
	p := adaptiveCPUPolicy{}
	s := adaptiveCPUSample{busy: 0.9, cpuKnown: true, foregroundKnown: true}
	if p.next(now, s) || p.next(now.Add(5*time.Second), s) {
		t.Fatal("brief CPU pressure changed scheduling")
	}
	if !p.next(now.Add(adaptiveCPUPressureDelay), s) {
		t.Fatal("sustained background pressure did not yield")
	}
	s.busy = 0.4
	if !p.next(now.Add(8*time.Second), s) || !p.next(now.Add(17*time.Second), s) {
		t.Fatal("brief quiet period restored priority too early")
	}
	if p.next(now.Add(18*time.Second), s) {
		t.Fatal("stable quiet period did not restore normal scheduling")
	}
}

func TestAdaptiveCPUForegroundAndUnknownSamplesRestoreImmediately(t *testing.T) {
	for _, sample := range []adaptiveCPUSample{
		{busy: .99, cpuKnown: true, foregroundKnown: true, omarchyForeground: true},
		{busy: .99, cpuKnown: false, foregroundKnown: true},
		{busy: .99, cpuKnown: true, foregroundKnown: false},
		{busy: math.NaN(), cpuKnown: true, foregroundKnown: true},
		{busy: math.Inf(1), cpuKnown: true, foregroundKnown: true},
		{busy: 1.1, cpuKnown: true, foregroundKnown: true},
		{busy: -.1, cpuKnown: true, foregroundKnown: true},
	} {
		p := adaptiveCPUPolicy{yielding: true, pressureSince: time.Unix(10, 0), clearSince: time.Unix(20, 0)}
		if p.next(time.Unix(1000, 0), sample) || p != (adaptiveCPUPolicy{}) {
			t.Fatalf("sample did not restore normal scheduling: %+v, policy=%+v", sample, p)
		}
	}
}

func TestAdaptiveCPUHysteresisAndInterruptedPressure(t *testing.T) {
	now := time.Unix(1000, 0)
	p := adaptiveCPUPolicy{}
	s := adaptiveCPUSample{busy: .8, cpuKnown: true, foregroundKnown: true}
	p.next(now, s)
	s.busy = .6
	p.next(now.Add(5*time.Second), s)
	s.busy = .8
	if p.next(now.Add(6*time.Second), s) || !p.next(now.Add(12*time.Second), s) {
		t.Fatal("interrupted pressure reused an old deadline")
	}
	s.busy = .6
	if !p.next(now.Add(20*time.Second), s) || !p.next(now.Add(100*time.Second), s) {
		t.Fatal("middle load range should preserve current yielding state")
	}
	s.omarchyForeground = true
	if p.next(now.Add(101*time.Second), s) {
		t.Fatal("foreground Omarchy should immediately regain normal priority")
	}
}
