//go:build windows

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsShortcutArguments(t *testing.T) {
	defaultDir := filepath.Join(os.Getenv("LOCALAPPDATA"), defaultDataDirectoryName)
	if got := settingsShortcutArguments(defaultDir); got != "-settings" {
		t.Fatalf("default settings shortcut arguments = %q", got)
	}
	custom := `D:\Try Omarchy Test`
	want := `-dir "D:\Try Omarchy Test" -settings`
	if got := settingsShortcutArguments(custom); got != want {
		t.Fatalf("custom settings shortcut arguments = %q, want %q", got, want)
	}
}

func TestLauncherShortcutNamesKeepLegacyPathsForCleanup(t *testing.T) {
	current, err := launcherShortcutPaths()
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := legacyLauncherShortcutPaths()
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"Omarchy.lnk", "Omarchy Settings.lnk", "Omarchy.lnk"} {
		if got := filepath.Base(current[i]); got != want {
			t.Fatalf("current shortcut %d = %q; want %q", i, got, want)
		}
	}
	for i, want := range []string{"Try Omarchy.lnk", "Try Omarchy Settings.lnk", "Try Omarchy.lnk"} {
		if got := filepath.Base(legacy[i]); got != want {
			t.Fatalf("legacy shortcut %d = %q; want %q", i, got, want)
		}
	}
}

func TestCreateLauncherShortcutsCreatesOneAppEntry(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, stableLauncherName)
	if err := os.WriteFile(target, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	paths := shortcutPathsIn(dir, dir, "Omarchy", "Omarchy Settings")
	if err := createLauncherShortcutsAtPaths(paths, target, dir, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatalf("main shortcut missing: %v", err)
	}
	if _, err := os.Stat(paths[1]); !os.IsNotExist(err) {
		t.Fatalf("Settings shortcut created: %v", err)
	}
}

func TestMigrateOnlyOwnedLauncherShortcuts(t *testing.T) {
	dir := t.TempDir()
	programs := filepath.Join(dir, "Start Menu")
	desktop := filepath.Join(dir, "Desktop")
	for _, folder := range []string{programs, desktop} {
		if err := os.MkdirAll(folder, 0700); err != nil {
			t.Fatal(err)
		}
	}
	target := filepath.Join(dir, stableLauncherName)
	foreign := filepath.Join(dir, "other.exe")
	for _, path := range []string{target, foreign} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := shortcutPathsIn(programs, desktop, "Try Omarchy", "Try Omarchy Settings")
	current := shortcutPathsIn(programs, desktop, "Omarchy", "Omarchy Settings")
	for i, path := range legacy {
		linked := target
		args := ""
		if i == 1 {
			args = "-settings"
		}
		if i == 2 {
			linked = foreign
		}
		if err := writeShellLink(path, linked, args, dir); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateOwnedLauncherShortcuts(legacy, current, target, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy[0]); !os.IsNotExist(err) {
		t.Fatalf("owned legacy shortcut %q remains", legacy[0])
	}
	gotTarget, _, err := readShellLink(current[0])
	if err != nil || !sameShortcutTarget(gotTarget, target) {
		t.Fatalf("migrated shortcut %q: target %q, %v", current[0], gotTarget, err)
	}
	if _, err := os.Stat(current[1]); !os.IsNotExist(err) {
		t.Fatalf("Settings shortcut was created during migration: %v", err)
	}
	if err := removeOwnedSettingsShortcuts([]string{legacy[1], current[1]}, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy[1]); !os.IsNotExist(err) {
		t.Fatalf("owned legacy Settings shortcut remains: %v", err)
	}
	if _, err := os.Stat(legacy[2]); err != nil {
		t.Fatalf("foreign legacy shortcut was removed: %v", err)
	}
	if _, err := os.Stat(current[2]); !os.IsNotExist(err) {
		t.Fatalf("a desktop shortcut was created for another install: %v", err)
	}
}

func TestObsoleteSettingsCleanupKeepsOtherInstall(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, stableLauncherName)
	foreign := filepath.Join(dir, "other.exe")
	for _, path := range []string{target, foreign} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(dir, "Omarchy Settings.lnk")
	legacy := filepath.Join(dir, "Try Omarchy Settings.lnk")
	if err := writeShellLink(current, target, "-settings", dir); err != nil {
		t.Fatal(err)
	}
	if err := writeShellLink(legacy, foreign, "-settings", dir); err != nil {
		t.Fatal(err)
	}
	if err := removeOwnedSettingsShortcuts([]string{current, legacy}, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(current); !os.IsNotExist(err) {
		t.Fatalf("owned Settings shortcut remains: %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("other installation's Settings shortcut was removed: %v", err)
	}
}

func TestMigrationKeepsLegacyShortcutWhenNewNameIsTaken(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, stableLauncherName)
	foreign := filepath.Join(dir, "other.exe")
	for _, path := range []string{target, foreign} {
		if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	legacy := shortcutPathsIn(dir, dir, "Try Omarchy", "Try Omarchy Settings")
	current := shortcutPathsIn(dir, dir, "Omarchy", "Omarchy Settings")
	if err := writeShellLink(legacy[0], target, "", dir); err != nil {
		t.Fatal(err)
	}
	if err := writeShellLink(current[0], foreign, "", dir); err != nil {
		t.Fatal(err)
	}
	if err := migrateOwnedLauncherShortcuts(legacy[:1], current[:1], target, dir); err == nil {
		t.Fatal("foreign destination should block migration")
	}
	for _, path := range []string{legacy[0], current[0]} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("shortcut %q lost after a conflict: %v", path, err)
		}
	}
}
