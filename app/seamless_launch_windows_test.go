//go:build windows

package main

import (
	"syscall"
	"testing"
	"time"
)

func TestLaunchedProcessEligibleRequiresFreshDirectChildWithinRootLifetime(t *testing.T) {
	launch := seamlessLaunch{pid: 21836, created: 1000, handle: syscall.Handle(1)}
	tests := []struct {
		name       string
		pid        uint32
		parent     uint32
		created    uint64
		rootExit   uint64
		want       bool
	}{
		{"original launcher", 21836, 0, 1000, 0, true},
		{"reused launcher PID", 21836, 0, 1200, 1100, false},
		{"Blender direct child after launcher exits", 26960, 21836, 1010, 1020, true},
		{"Blender direct child while launcher runs", 26960, 21836, 1010, 0, true},
		{"old child process", 26960, 21836, 999, 1020, false},
		{"child of reused launcher PID", 26960, 21836, 1030, 1020, false},
		{"unrelated process with same name", 26960, 99999, 1010, 1020, false},
		{"grandchild needs explicit host choice", 26960, 12345, 1010, 1020, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := launchedProcessEligible(launch, tt.pid, tt.parent, tt.created, tt.rootExit); got != tt.want {
				t.Fatalf("eligible = %t; want %t", got, tt.want)
			}
		})
	}
	launch.handle = 0
	if launchedProcessEligible(launch, 26960, 21836, 1010, 1020) {
		t.Fatal("child granted without retained launcher identity")
	}
}

func TestClosedBridgeRejectsLateLaunch(t *testing.T) {
	bridge := &seamlessWindowBridge{}
	if !bridge.noteLaunch(21836, nil, time.Now()) {
		t.Fatal("initial launch was not tracked")
	}
	bridge.closePendingLaunches()
	if bridge.noteLaunch(26960, nil, time.Now()) || len(bridge.pending) != 0 {
		t.Fatal("closed bridge retained a late process launch")
	}
}
