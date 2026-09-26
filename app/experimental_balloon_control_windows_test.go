//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestExperimentalBalloonControlLifecycle(t *testing.T) {
	control, err := prepareExperimentalBalloonControl()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = control.Close() })
	path, err := control.validatedPath()
	if err != nil || path == "" {
		t.Fatalf("reserved endpoint: %q %v", path, err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err == nil {
		listener.Close()
		t.Fatal("removed a listening endpoint")
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(control.dir); !os.IsNotExist(err) {
		t.Fatalf("reserved directory remains: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("second cleanup: %v", err)
	}
}

func TestExperimentalBalloonControlRejectsReplacedDirectory(t *testing.T) {
	control, err := prepareExperimentalBalloonControl()
	if err != nil {
		t.Fatal(err)
	}
	other, err := prepareExperimentalBalloonControl()
	if err != nil {
		_ = control.Close()
		t.Fatal(err)
	}
	defer control.Close()
	defer other.Close()
	changed := *control
	changed.dir, changed.path = other.dir, other.path
	if _, err := changed.validatedPath(); err == nil {
		t.Fatal("accepted another directory with the first directory identity")
	}
	if err := changed.Close(); err == nil {
		t.Fatal("removed another directory")
	}
}

func TestExperimentalBalloonControlPreservesUnexpectedFile(t *testing.T) {
	control, err := prepareExperimentalBalloonControl()
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	defer os.Remove(control.path)
	if err := os.WriteFile(control.path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := control.Close(); err == nil {
		t.Fatal("removed ordinary file at socket path")
	}
	if data, err := os.ReadFile(control.path); err != nil || string(data) != "preserve" {
		t.Fatalf("ordinary file changed: %q %v", data, err)
	}
	changed := *control
	changed.path = filepath.Join(control.dir, "supervisor.sock")
	if _, err := changed.validatedPath(); err == nil {
		t.Fatal("accepted an alternate socket path")
	}
}
