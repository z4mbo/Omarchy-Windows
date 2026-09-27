//go:build windows

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPortableRuntimeUpdateRetainsOfflineRepairArchive(t *testing.T) {
	configureSetupCancellation(false)
	t.Cleanup(func() { configureSetupCancellation(false) })
	uiOnce.Do(func() { uiSingleton = &progressUI{} })
	dir, payload := portableRecoveryFixture(t)
	const release = "https://example.invalid/v0.0.101-preview"
	const executable = "bin/qemu-system-x86_64w.exe"
	newRuntime := []byte("candidate runtime, never executed")
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	f, err := w.Create(executable)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(newRuntime); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	archiveSHA := testSHA256(archive.Bytes())
	manifest := []byte(archiveSHA + "  " + runtimeZip + "\n")
	pin := testSHA256(manifest)
	versioned := filepath.Join(payload, pin)
	if err := os.Mkdir(versioned, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"SHA256SUMS": manifest, runtimeZip: archive.Bytes()} {
		if err := os.WriteFile(filepath.Join(versioned, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config{dir: dir, hostDir: dir, payloadDir: payload, portable: true}
	runtimeRoot, err := ensureRuntime(cfg, release, pin)
	if err != nil {
		t.Fatal(err)
	}
	if !runtimeReceiptMatches(runtimeRoot, release, pin, archiveSHA) {
		t.Fatal("candidate runtime receipt did not match")
	}
	retained, err := os.ReadFile(filepath.Join(versioned, runtimeZip))
	if err != nil || !bytes.Equal(retained, archive.Bytes()) {
		t.Fatalf("portable repair archive was removed or changed: %v", err)
	}
	previous, err := os.ReadFile(filepath.Join(dir, "runtime.previous", executable))
	if err != nil || string(previous) != "retained runtime" {
		t.Fatalf("previous runtime unavailable for rollback: %v", err)
	}
	// Exercise actual offline extraction again in a fresh repair destination.
	// No QEMU process or network endpoint is started by either setup call.
	repairDir := filepath.Join(t.TempDir(), "repaired")
	if err := os.Mkdir(repairDir, 0700); err != nil {
		t.Fatal(err)
	}
	repair := &config{dir: repairDir, hostDir: repairDir, payloadDir: payload, portable: true}
	repaired, err := ensureRuntime(repair, release, pin)
	if err != nil {
		t.Fatal("offline repair failed:", err)
	}
	actual, err := os.ReadFile(filepath.Join(repaired, executable))
	if err != nil || !bytes.Equal(actual, newRuntime) {
		t.Fatalf("offline repair did not restore the exact runtime: %v", err)
	}
}
