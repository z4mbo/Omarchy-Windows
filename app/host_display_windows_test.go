//go:build windows

package main

import (
	"testing"
	"unsafe"
)

func TestWindowsDisplayStructLayouts(t *testing.T) {
	if got := unsafe.Sizeof(monitorInfoExW{}); got != 104 {
		t.Fatalf("MONITORINFOEXW size = %d", got)
	}
	if got := unsafe.Sizeof(devModeW{}); got != 220 {
		t.Fatalf("DEVMODEW size = %d", got)
	}
	if got := unsafe.Offsetof(devModeW{}.DisplayFrequency); got != 184 {
		t.Fatalf("DEVMODEW frequency offset = %d", got)
	}
}

func TestMonitorForPlacementUsesLargestOverlap(t *testing.T) {
	monitors := []hostMonitorMode{
		{Bounds: screenRect{0, 0, 1920, 1080}},
		{Bounds: screenRect{1920, 0, 4480, 1440}},
	}
	if got := monitorForPlacement(nil, monitors, 1); got != 1 {
		t.Fatalf("default output 1 assigned to monitor %d", got)
	}
	p := &windowPlacement{Normal: screenRect{1800, 100, 3000, 900}}
	if got := monitorForPlacement(p, monitors, 0); got != 1 {
		t.Fatalf("restored window assigned to monitor %d", got)
	}
}

func TestCurrentHostMonitorMode(t *testing.T) {
	modes := currentHostMonitors()
	if len(modes) == 0 {
		t.Skip("no attached Windows monitor")
	}
	for i, mode := range modes {
		t.Logf("monitor %d: bounds=%+v work=%+v refresh=%d mHz", i, mode.Bounds, mode.Work, mode.RefreshMilliHz)
		if mode.Bounds.width() < 640 || mode.Bounds.height() < 480 || mode.Work.width() <= 0 || mode.Work.height() <= 0 {
			t.Fatalf("invalid monitor %d bounds: %+v", i, mode)
		}
		if mode.RefreshMilliHz != 0 && (mode.RefreshMilliHz < 24000 || mode.RefreshMilliHz > 1000000) {
			t.Fatalf("invalid monitor %d refresh: %d", i, mode.RefreshMilliHz)
		}
	}
}
