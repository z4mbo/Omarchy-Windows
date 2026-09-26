package main

import (
	"strings"
	"testing"
)

func TestSDLDisplayAdvertisesHostRefreshInMilliHz(t *testing.T) {
	got := sdlDisplayWithRefresh(true, false, 360000)
	if !strings.Contains(got, "refresh-rate=360000") {
		t.Fatalf("missing virtual EDID rate: %q", got)
	}
	for _, unknown := range []int{0, 1000, 1000001} {
		if got := sdlDisplayWithRefresh(false, false, unknown); strings.Contains(got, "refresh-rate=") {
			t.Fatalf("invalid refresh %d leaked into QEMU: %q", unknown, got)
		}
	}
}

func TestDisplayRefreshRequiresCompatibleRuntime(t *testing.T) {
	cfg := &config{runtimeID: "winq-emu", displayRefreshMilliHz: 144000, memMiB: 1024, cpus: 2}
	findDisplay := func() string {
		args := buildQemuArgs(cfg, "")
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "-display" {
				return args[i+1]
			}
		}
		return ""
	}
	if got := findDisplay(); !strings.Contains(got, "refresh-rate=144000") {
		t.Fatalf("WINQ-EMU display did not advertise 144 Hz: %q", got)
	}
	cfg.runtimeID = "" // stock QEMU does not implement this WINQ-EMU option
	if got := findDisplay(); strings.Contains(got, "refresh-rate=") {
		t.Fatalf("stock QEMU got an unsupported option: %q", got)
	}
}
