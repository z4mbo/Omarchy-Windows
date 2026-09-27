//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestAdaptiveCPUChild(t *testing.T) {
	if os.Getenv("OMARCHY_CPU_PRIORITY_TEST_CHILD") != "1" {
		t.Skip("helper process")
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestAdaptiveCPUWindowsPriorityAndManualOverride(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestAdaptiveCPUChild$")
	cmd.Env = append(os.Environ(), "OMARCHY_CPU_PRIORITY_TEST_CHILD=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		in.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { in.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	process, err := openAdaptiveCPUProcess(uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(process.handle)
	if err := process.setPriority(adaptiveCPUNormal); err != nil {
		t.Fatal(err)
	}
	assertPriority := func(want uint32) {
		t.Helper()
		got, err := process.priority()
		if err != nil || got != want {
			t.Fatalf("process priority = %#x, err=%v; want %#x", got, err, want)
		}
	}
	controller := adaptiveCPUController{process: process, expected: adaptiveCPUNormal}
	now := time.Now()
	pressure := adaptiveCPUSample{busy: .95, cpuKnown: true, foregroundKnown: true}
	if err := controller.tick(now, pressure); err != nil {
		t.Fatal(err)
	}
	if err := controller.tick(now.Add(adaptiveCPUPressureDelay), pressure); err != nil {
		t.Fatal(err)
	}
	assertPriority(adaptiveCPUBelowNormal)
	foreground := pressure
	foreground.omarchyForeground = true
	if err := controller.tick(now.Add(7*time.Second), foreground); err != nil {
		t.Fatal(err)
	}
	assertPriority(adaptiveCPUNormal)
	if err := controller.tick(now.Add(8*time.Second), pressure); err != nil {
		t.Fatal(err)
	}
	if err := controller.tick(now.Add(14*time.Second), pressure); err != nil {
		t.Fatal(err)
	}
	controller.restore()
	assertPriority(adaptiveCPUNormal)

	// A manual change must stop the controller and survive cleanup.
	controller = adaptiveCPUController{process: process, expected: adaptiveCPUNormal}
	if err := process.setPriority(adaptiveCPUBelowNormal); err != nil {
		t.Fatal(err)
	}
	if err := controller.tick(now, foreground); !errors.Is(err, errAdaptiveCPUOverride) {
		t.Fatalf("manual override error = %v", err)
	}
	controller.restore()
	assertPriority(adaptiveCPUBelowNormal)
	stop := startAdaptiveCPUScheduling(uint32(cmd.Process.Pid))
	stop()
	assertPriority(adaptiveCPUBelowNormal)
}
