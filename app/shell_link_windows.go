//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

// WScript.Shell rejects targets outside the system ANSI code page on some
// Windows installations. Use IShellLinkW so installation paths stay Unicode.
func writeShellLink(path, target, arguments, directory string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	check := func(hr uintptr) error {
		if int32(hr) < 0 {
			return fmt.Errorf("creating Windows shortcut: HRESULT 0x%08x", uint32(hr))
		}
		return nil
	}
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if uint32(hr) != 0x80010106 { // An existing COM apartment is also usable.
		if err := check(hr); err != nil {
			return err
		}
		defer ole32.NewProc("CoUninitialize").Call()
	}
	class := recoveryGUID{0x00021401, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iid := recoveryGUID{0x000214f9, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var link uintptr
	hr, _, _ = ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&link)))
	if err := check(hr); err != nil {
		return err
	}
	defer recoveryCOMCall(link, 2)
	description := "Run Omarchy on Windows"
	for _, arg := range strings.Fields(arguments) {
		if arg == "-settings" {
			description = "Configure Omarchy"
		}
	}
	for _, property := range []struct {
		method int
		value  string
	}{{20, target}, {11, arguments}, {9, directory}, {7, description}} {
		value, err := syscall.UTF16PtrFromString(property.value)
		if err != nil {
			return err
		}
		if err := check(recoveryCOMCall(link, property.method, uintptr(unsafe.Pointer(value)))); err != nil {
			return err
		}
	}
	icon, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	if err := check(recoveryCOMCall(link, 17, uintptr(unsafe.Pointer(icon)), 0)); err != nil {
		return err
	}
	persistIID := recoveryGUID{0x0000010b, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var persist uintptr
	if err := check(recoveryCOMCall(link, 0, uintptr(unsafe.Pointer(&persistIID)), uintptr(unsafe.Pointer(&persist)))); err != nil {
		return err
	}
	defer recoveryCOMCall(persist, 2)
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return check(recoveryCOMCall(persist, 6, uintptr(unsafe.Pointer(name)), 1))
}

func readShellLink(path string) (target, arguments string, err error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	check := func(hr uintptr) error {
		if int32(hr) < 0 {
			return fmt.Errorf("reading Windows shortcut: HRESULT 0x%08x", uint32(hr))
		}
		return nil
	}
	hr, _, _ := ole32.NewProc("CoInitializeEx").Call(0, 2)
	if uint32(hr) != 0x80010106 {
		if err = check(hr); err != nil {
			return
		}
		defer ole32.NewProc("CoUninitialize").Call()
	}
	class := recoveryGUID{0x00021401, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	iid := recoveryGUID{0x000214f9, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var link uintptr
	hr, _, _ = ole32.NewProc("CoCreateInstance").Call(uintptr(unsafe.Pointer(&class)), 0, 1, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&link)))
	if err = check(hr); err != nil {
		return
	}
	defer recoveryCOMCall(link, 2)
	persistIID := recoveryGUID{0x0000010b, 0, 0, [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	var persist uintptr
	if err = check(recoveryCOMCall(link, 0, uintptr(unsafe.Pointer(&persistIID)), uintptr(unsafe.Pointer(&persist)))); err != nil {
		return
	}
	defer recoveryCOMCall(persist, 2)
	name, e := syscall.UTF16PtrFromString(path)
	if e != nil {
		return "", "", e
	}
	if err = check(recoveryCOMCall(persist, 5, uintptr(unsafe.Pointer(name)), 0)); err != nil {
		return
	}
	buffer := make([]uint16, 32768)
	if err = check(recoveryCOMCall(link, 3, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0, 4)); err != nil {
		return
	}
	target = syscall.UTF16ToString(buffer)
	clear(buffer)
	if err = check(recoveryCOMCall(link, 10, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))); err != nil {
		return
	}
	arguments = syscall.UTF16ToString(buffer)
	return
}

func launcherShortcutFolders() ([]string, error) {
	folders := make([]string, 2)
	for i, csidl := range []uintptr{2, 0x10} { // Current user's Programs and Desktop.
		buffer := make([]uint16, 260)
		hr, _, _ := shell32.NewProc("SHGetFolderPathW").Call(0, csidl, 0, 0, uintptr(unsafe.Pointer(&buffer[0])))
		if int32(hr) < 0 {
			return nil, fmt.Errorf("locating shortcut folder: HRESULT 0x%08x", uint32(hr))
		}
		folders[i] = syscall.UTF16ToString(buffer)
		if folders[i] == "" {
			return nil, fmt.Errorf("Windows returned an empty shortcut folder")
		}
	}
	return folders, nil
}

func launcherShortcutPaths() ([]string, error) {
	folders, err := launcherShortcutFolders()
	if err != nil {
		return nil, err
	}
	return shortcutPathsIn(folders[0], folders[1], "Omarchy", "Omarchy Settings"), nil
}

func legacyLauncherShortcutPaths() ([]string, error) {
	folders, err := launcherShortcutFolders()
	if err != nil {
		return nil, err
	}
	return shortcutPathsIn(folders[0], folders[1], "Try Omarchy", "Try Omarchy Settings"), nil
}

func shortcutPathsIn(programs, desktop, name, settings string) []string {
	return []string{filepath.Join(programs, name+".lnk"), filepath.Join(programs, settings+".lnk"), filepath.Join(desktop, name+".lnk")}
}

// Existing installations recorded their shortcut offer, so migrate the links
// during normal launch. Only links targeting this installation may be renamed.
// Write the new link before removing the old one so a failed write leaves a
// working shortcut behind.
func migrateOwnedLauncherShortcuts(legacy, current []string, target, dir string) error {
	if len(legacy) != len(current) {
		return fmt.Errorf("shortcut lists have different lengths")
	}
	for i, oldPath := range legacy {
		if i == 1 { // Settings now lives inside the single Omarchy app.
			continue
		}
		if _, err := os.Lstat(oldPath); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		oldTarget, args, err := readShellLink(oldPath)
		if err != nil {
			return err
		}
		if !sameShortcutTarget(oldTarget, target) {
			continue
		}
		if _, err := os.Lstat(current[i]); err == nil {
			newTarget, _, err := readShellLink(current[i])
			if err != nil {
				return err
			}
			if !sameShortcutTarget(newTarget, target) {
				return fmt.Errorf("shortcut already belongs to another installation: %s", current[i])
			}
		} else if os.IsNotExist(err) {
			if err := writeShellLink(current[i], target, args, dir); err != nil {
				return err
			}
		} else {
			return err
		}
		if err := os.Remove(oldPath); err != nil {
			return err
		}
	}
	return nil
}

func removeOwnedSettingsShortcuts(paths []string, target string) error {
	return changeOwnedShortcuts(paths, []string{target}, func(path, _ string) error {
		return os.Remove(path)
	})
}

// Read identity with the Unicode API before modifying a link. Never resolve or
// execute the target; a different installation's shortcut must remain intact.
func changeOwnedShortcuts(paths, targets []string, change func(string, string) error) error {
	for _, path := range paths {
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		target, args, err := readShellLink(path)
		if err != nil {
			return err
		}
		for _, owned := range targets {
			if sameShortcutTarget(target, owned) {
				if err := change(path, args); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

// Shell links can expand a short (8.3) path even when SetPath received the
// short spelling. Compare existing file identity as well as literal paths.
// Keep the literal comparison for a retained link whose target is now absent.
func sameShortcutTarget(target, owned string) bool {
	if pathsEqual(target, owned) {
		return true
	}
	actual, err := os.Stat(target)
	if err != nil {
		return false
	}
	expected, err := os.Stat(owned)
	return err == nil && os.SameFile(actual, expected)
}
