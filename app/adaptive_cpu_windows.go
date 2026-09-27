//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"
	"unsafe"
)

const (
	adaptiveCPUNormal      = uint32(0x20)
	adaptiveCPUBelowNormal = uint32(0x4000)
)

var (
	procGetPriorityClass   = kernel32.NewProc("GetPriorityClass")
	procSetPriorityClass   = kernel32.NewProc("SetPriorityClass")
	errAdaptiveCPUOverride = errors.New("QEMU priority was changed externally")
)

type adaptiveCPUProcess struct{ handle syscall.Handle }

func openAdaptiveCPUProcess(pid uint32) (*adaptiveCPUProcess, error) {
	if pid == 0 || pid == uint32(os.Getpid()) {
		return nil, fmt.Errorf("invalid guest process")
	}
	// QUERY_LIMITED_INFORMATION | SET_INFORMATION | SYNCHRONIZE. Retaining a
	// process handle keeps all later operations bound to this process lifetime.
	h, err := syscall.OpenProcess(0x1000|0x0200|0x100000, false, pid)
	if err != nil {
		return nil, err
	}
	return &adaptiveCPUProcess{handle: h}, nil
}

func (p *adaptiveCPUProcess) running() bool {
	state, err := syscall.WaitForSingleObject(p.handle, 0)
	return err == nil && state == 258 // WAIT_TIMEOUT
}

func (p *adaptiveCPUProcess) priority() (uint32, error) {
	value, _, err := procGetPriorityClass.Call(uintptr(p.handle))
	if value == 0 {
		return 0, fmt.Errorf("reading guest CPU priority: %w", err)
	}
	return uint32(value), nil
}

func (p *adaptiveCPUProcess) setPriority(priority uint32) error {
	if priority != adaptiveCPUNormal && priority != adaptiveCPUBelowNormal {
		return fmt.Errorf("unsupported adaptive CPU priority")
	}
	ok, _, err := procSetPriorityClass.Call(uintptr(p.handle), uintptr(priority))
	if ok == 0 {
		return fmt.Errorf("setting guest CPU priority: %w", err)
	}
	return nil
}

type adaptiveCPUController struct {
	process  *adaptiveCPUProcess
	policy   adaptiveCPUPolicy
	expected uint32
}

func (c *adaptiveCPUController) tick(now time.Time, sample adaptiveCPUSample) error {
	if !c.process.running() {
		return os.ErrProcessDone
	}
	current, err := c.process.priority()
	if err != nil {
		return err
	}
	// Respect Task Manager or another explicit process-priority change.
	if current != c.expected {
		return errAdaptiveCPUOverride
	}
	wanted := adaptiveCPUNormal
	if c.policy.next(now, sample) {
		wanted = adaptiveCPUBelowNormal
	}
	if wanted == current {
		return nil
	}
	if err := c.process.setPriority(wanted); err != nil {
		return err
	}
	c.expected = wanted
	logf("resources: guest CPU scheduling yielding=%t, host busy=%.1f%%", wanted == adaptiveCPUBelowNormal, sample.busy*100)
	return nil
}

func (c *adaptiveCPUController) restore() {
	if c.expected == adaptiveCPUNormal || !c.process.running() {
		return
	}
	if current, err := c.process.priority(); err == nil && current == c.expected {
		if err := c.process.setPriority(adaptiveCPUNormal); err != nil {
			logf("resources: could not restore guest CPU priority: %v", err)
		}
	}
}

func adaptiveCPUForeground(pid uint32) (omarchy, known bool) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return false, false
	}
	var owner uint32
	thread, _, _ := procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&owner)))
	if thread == 0 || owner == 0 {
		return false, false
	}
	return owner == pid || owner == uint32(os.Getpid()), true
}

// startAdaptiveCPUScheduling only manages a child that starts at Normal.
// Automatic profiles yield under sustained host load while another app is in
// the foreground. Manual resource profiles do not start this controller.
func startAdaptiveCPUScheduling(pid uint32) func() {
	noop := func() {}
	if runtime.NumCPU() > 64 {
		// GetSystemTimes does not describe every processor group on these hosts.
		return noop
	}
	process, err := openAdaptiveCPUProcess(pid)
	if err != nil {
		logf("resources: adaptive CPU scheduling unavailable: %v", err)
		return noop
	}
	priority, err := process.priority()
	if err != nil || priority != adaptiveCPUNormal {
		syscall.CloseHandle(process.handle)
		logf("resources: retaining existing guest CPU priority (%#x)", priority)
		return noop
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer syscall.CloseHandle(process.handle)
		controller := adaptiveCPUController{process: process, expected: priority}
		defer controller.restore()
		i0, k0, u0, previousKnown := systemCPUTimes()
		ticker := time.NewTicker(adaptiveCPUInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				i1, k1, u1, known := systemCPUTimes()
				sample := adaptiveCPUSample{}
				if known && previousKnown {
					sample.busy, sample.cpuKnown = cpuBusyFraction(i0, k0, u0, i1, k1, u1)
				}
				i0, k0, u0, previousKnown = i1, k1, u1, known
				sample.omarchyForeground, sample.foregroundKnown = adaptiveCPUForeground(pid)
				if err := controller.tick(now, sample); err != nil {
					if !errors.Is(err, os.ErrProcessDone) {
						logf("resources: adaptive CPU scheduling stopped: %v", err)
					}
					return
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
