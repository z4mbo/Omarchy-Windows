//go:build windows

package main

import (
	"errors"
	"syscall"
	"unsafe"
)

// A fresh launcher may create the visible app in a direct child process, as
// Blender's Start Menu launcher does. Parent PIDs alone are not identities:
// the retained root handle and creation/exit times bound the child to this
// launch even if Windows later reuses the root PID.
func seamlessProcessParents() (map[uint32]uint32, error) {
	snapshot, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer syscall.CloseHandle(snapshot)
	parents := make(map[uint32]uint32)
	entry := syscall.ProcessEntry32{Size: uint32(unsafe.Sizeof(syscall.ProcessEntry32{}))}
	if err := syscall.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	for {
		parents[entry.ProcessID] = entry.ParentProcessID
		entry.Size = uint32(unsafe.Sizeof(entry))
		err = syscall.Process32Next(snapshot, &entry)
		if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
			return parents, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// Zero exit time means that the retained launcher process is still alive.
// Any failed wait or identity read prevents a descendant grant.
func seamlessProcessExit(handle syscall.Handle) (uint64, bool) {
	if handle == 0 {
		return 0, false
	}
	state, err := syscall.WaitForSingleObject(handle, 0)
	if err != nil {
		return 0, false
	}
	if state == syscall.WAIT_TIMEOUT {
		return 0, true
	}
	if state != syscall.WAIT_OBJECT_0 {
		return 0, false
	}
	var created, exited, kernelTime, userTime syscall.Filetime
	ok, _, _ := hostAppGetProcessTimes.Call(uintptr(handle),
		uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernelTime)), uintptr(unsafe.Pointer(&userTime)))
	if ok == 0 {
		return 0, false
	}
	exitTime := uint64(exited.HighDateTime)<<32 | uint64(exited.LowDateTime)
	return exitTime, exitTime != 0
}

func launchedProcessEligible(launch seamlessLaunch, candidatePID, parentPID uint32, candidateCreated, rootExit uint64) bool {
	if candidatePID == launch.pid {
		return launch.created == 0 || candidateCreated == launch.created
	}
	if launch.created == 0 || launch.handle == 0 || parentPID != launch.pid || candidateCreated <= launch.created {
		return false
	}
	return rootExit == 0 || candidateCreated <= rootExit
}
