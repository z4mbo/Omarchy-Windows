//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var procMoveFileExW = kernel32.NewProc("MoveFileExW")

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

func replaceLauncher(staged, target string) error {
	from, err := syscall.UTF16PtrFromString(staged)
	if err != nil {
		return err
	}
	to, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r, _, callErr := procMoveFileExW.Call(
		uintptr(unsafe.Pointer(from)), uintptr(unsafe.Pointer(to)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if r == 0 {
		return callErr
	}
	return nil
}

func stableLauncherPath(dir string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	target := filepath.Join(dir, stableLauncherName)
	if err := copyLauncher(self, target, replaceLauncher); err != nil {
		return "", err
	}
	return target, nil
}

func shortcutArguments(dir string) string {
	defaultDir := filepath.Join(os.Getenv("LOCALAPPDATA"), defaultDataDirectoryName)
	if pathsEqual(dir, defaultDir) {
		return ""
	}
	// Double quotes cannot occur in a Windows path. Refuse to create a broken
	// shortcut if a synthetic command-line value somehow contains one.
	if strings.ContainsRune(dir, '"') {
		return ""
	}
	return `-dir "` + dir + `"`
}

func settingsShortcutArguments(dir string) string {
	return strings.TrimSpace(shortcutArguments(dir) + " -settings")
}

func writeOwnedOrNewShellLink(path, target, args, dir string) error {
	if _, err := os.Lstat(path); err == nil {
		existing, _, err := readShellLink(path)
		if err != nil {
			return err
		}
		if !sameShortcutTarget(existing, target) {
			return fmt.Errorf("shortcut already belongs to another installation: %s", path)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeShellLink(path, target, args, dir)
}

func createLauncherShortcuts(target, dir string, startMenu, desktop bool) error {
	if !startMenu && !desktop {
		return nil
	}
	paths, err := launcherShortcutPaths()
	if err != nil {
		return err
	}
	return createLauncherShortcutsAtPaths(paths, target, dir, startMenu, desktop)
}

func createLauncherShortcutsAtPaths(paths []string, target, dir string, startMenu, desktop bool) error {
	for i, path := range paths {
		if i == 1 || i == 0 && !startMenu || i == 2 && !desktop {
			continue
		}
		if err := writeOwnedOrNewShellLink(path, target, shortcutArguments(dir), dir); err != nil {
			return err
		}
	}
	return nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func chooseProvisionMode(cfg *config, newInstall bool) {
	if cfg.instant {
		getUI().setInstantMode(true)
		if err := writeProvisionMode(cfg.dir, provisionModeInstant); err != nil {
			fatal("Could not save the instant trial choice: %v", err)
		}
		return
	}
	// -fresh creates a new writable guest, so let the user choose again instead
	// of silently inheriting the previous guest's first-boot mode.
	if mode, ok := readProvisionMode(cfg.dir); ok && !cfg.fresh {
		cfg.instant = mode == provisionModeInstant
		getUI().setInstantMode(cfg.instant)
		return
	}
	if !newInstall {
		return
	}
	mode := provisionModePersonal
	if getUI().chooseInstantMode() {
		mode = provisionModeInstant
		cfg.instant = true
	}
	if setupCancelled() {
		return
	}
	getUI().setInstantMode(cfg.instant)
	if err := writeProvisionMode(cfg.dir, mode); err != nil {
		fatal("Could not save the first-boot choice: %v", err)
	}
}

// offerLauncherShortcuts runs only after the guest and writable disk are
// complete. The signed launcher is copied into the app-data folder on every
// successful launch, so opening a newer downloaded release refreshes the
// stable target without making existing shortcuts fragile.
func offerLauncherShortcuts(dir string) {
	installDir, err := filepath.Abs(dir)
	if err != nil {
		logf("stable launcher path: %v", err)
		return
	}
	target, err := stableLauncherPath(installDir)
	if err != nil {
		logf("stable launcher: %v", err)
		return
	}
	paths, pathErr := launcherShortcutPaths()
	legacy, legacyErr := legacyLauncherShortcutPaths()
	if pathErr == nil && legacyErr == nil {
		if err := migrateOwnedLauncherShortcuts(legacy, paths, target, installDir); err != nil {
			logf("shortcut migration: %v", err)
		}
	} else {
		logf("shortcut migration: %v %v", pathErr, legacyErr)
	}
	if pathErr == nil && legacyErr == nil {
		settingsPaths := []string{
			paths[1], legacy[1],
			filepath.Join(filepath.Dir(paths[2]), "Omarchy Settings.lnk"),
			filepath.Join(filepath.Dir(legacy[2]), "Try Omarchy Settings.lnk"),
		}
		if err := removeOwnedSettingsShortcuts(settingsPaths, target); err != nil {
			logf("obsolete settings shortcuts: %v", err)
		}
	}
	if err := registerUninstallEntry(target, installDir); err != nil {
		logf("apps & features entry: %v", err)
	}
	if shortcutOfferRecorded(installDir) {
		return
	}
	startMenu, desktop := getUI().chooseShortcuts()
	if setupCancelled() {
		return
	}
	if err := createLauncherShortcuts(target, installDir, startMenu, desktop); err != nil {
		logf("shortcuts: %v", err)
		errorBox("Omarchy is ready, but Windows could not create the requested shortcut. You can keep using the downloaded launcher.\n\n" + err.Error())
		return
	}
	if err := recordShortcutOffer(installDir); err != nil {
		logf("shortcut offer marker: %v", err)
	}
}
