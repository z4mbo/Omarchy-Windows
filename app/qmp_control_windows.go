//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func platformQMPControlDirectory() (string, error) {
	// Some Windows installations allow AF_UNIX endpoints under the per-user
	// Temp directory but reject them under LocalAppData with CreateFile errors.
	// Temp is per-user here, and prepareQMPControl keeps the child directory
	// private and verifies stale endpoints before removing them.
	cache := os.TempDir()
	if len([]byte(filepath.Join(cache, "TryOmarchyIPC", "supervisor.sock"))) > 103 {
		name, err := syscall.UTF16PtrFromString(cache)
		if err != nil {
			return "", err
		}
		buffer := make([]uint16, 32768)
		if n, err := syscall.GetShortPathName(name, &buffer[0], uint32(len(buffer))); err == nil && n > 0 && n < uint32(len(buffer)) {
			cache = syscall.UTF16ToString(buffer[:n])
		}
	}
	return filepath.Join(cache, "TryOmarchyIPC"), nil
}

func isQMPControlSocket(path string) bool {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	var data syscall.Win32finddata
	handle, err := syscall.FindFirstFile(name, &data)
	if err != nil {
		return false
	}
	syscall.FindClose(handle)
	return data.FileAttributes&fileAttributeReparsePoint != 0 && data.Reserved0 == 0x80000023
}

func qmpConnectionRefused(err error) bool { return errors.Is(err, syscall.Errno(10061)) }
