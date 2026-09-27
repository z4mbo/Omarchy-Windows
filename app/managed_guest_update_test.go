package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func managedGuestUpdateFixture(t *testing.T) (managedGuestUpdateCoordinator, *guestUpdatePlan, map[string]string) {
	t.Helper()
	store := checkpointFixture(t)
	plan, _, _ := validGuestUpdatePlanForTest()
	guest := filepath.Join(store.installation, "guest")
	sums := make(map[string]string)
	for _, name := range installedGuestArtifacts {
		data, err := os.ReadFile(filepath.Join(guest, name))
		if err != nil {
			t.Fatal(err)
		}
		sums[name] = testSHA256(data)
	}
	if err := writeInstallReceipt(guest, forkReleaseBase+plan.Baseline.Release,
		plan.Baseline.ReleaseManifestSHA256, installedGuestArtifacts, sums); err != nil {
		t.Fatal(err)
	}
	return managedGuestUpdateCoordinator{installation: store.installation}, plan, sums
}

func TestManagedGuestUpdateRecoversPartialDiskWriteAfterRestart(t *testing.T) {
	c, plan, sums := managedGuestUpdateFixture(t)
	disk := filepath.Join(c.installation, "vm", "disk.raw")
	before, err := os.ReadFile(disk)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := c.prepareVerifiedPlan(plan, sums, nil)
	if err != nil {
		t.Fatal(err)
	}
	if journal.CheckpointID == "" || journal.PlanSHA256 == "" {
		t.Fatal("preparation returned no checkpoint or plan binding")
	}
	if _, err := c.prepareVerifiedPlan(plan, sums, nil); err == nil {
		t.Fatal("prepared a second update over a pending one")
	}
	// Simulate a crash after the future package executor has partially written
	// the guest disk. A new coordinator instance must recover before launch.
	if err := os.WriteFile(disk, []byte("partially written package"), 0600); err != nil {
		t.Fatal(err)
	}
	buildSpec := filepath.Join(c.installation, "guest", "build-spec.json")
	runtimeFile := filepath.Join(c.installation, "runtime", "bin", "qemu.exe")
	originalSpec, _ := os.ReadFile(buildSpec)
	originalRuntime, _ := os.ReadFile(runtimeFile)
	if err := os.WriteFile(buildSpec, []byte("torn build metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("torn runtime"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := (managedGuestUpdateCoordinator{installation: c.installation}).RecoverPending(nil)
	if err != nil || !recovered {
		t.Fatalf("recovery: %v, recovered=%v", err, recovered)
	}
	after, err := os.ReadFile(disk)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("disk not restored: %v", err)
	}
	if restored, _ := os.ReadFile(buildSpec); !bytes.Equal(restored, originalSpec) {
		t.Fatal("did not restore build metadata")
	}
	if restored, _ := os.ReadFile(runtimeFile); !bytes.Equal(restored, originalRuntime) {
		t.Fatal("did not restore runtime")
	}
	if _, err := os.Lstat(c.journalPath()); !os.IsNotExist(err) {
		t.Fatalf("completed recovery retained blocking journal: %v", err)
	}
	if again, err := c.RecoverPending(nil); err != nil || again {
		t.Fatalf("recovery was not idempotent: %v, %v", again, err)
	}
}

func TestManagedGuestUpdatePortableRecoveryUsesRestoredTool(t *testing.T) {
	tool := os.Getenv("QEMU_IMG")
	if tool == "" {
		var err error
		tool, err = exec.LookPath("qemu-img")
		if err != nil {
			t.Skip("qemu-img is unavailable on this test host")
		}
	}
	c, plan, sums := managedGuestUpdateFixture(t)
	name := "qemu-img"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	installedTool := filepath.Join(c.installation, "runtime", "bin", name)
	toolBytes, err := os.ReadFile(tool)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedTool, toolBytes, 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		// QEMU's Windows executable loads DLLs beside the executable. Keep the
		// fixture self-contained so recovery cannot accidentally use torn DLLs
		// from the original runtime through PATH.
		entries, err := os.ReadDir(filepath.Dir(tool))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(filepath.Dir(tool), entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(filepath.Dir(installedTool), entry.Name()), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	originalRaw, err := os.ReadFile(filepath.Join(c.installation, "vm", "disk.raw"))
	if err != nil {
		t.Fatal(err)
	}
	if err := makeRestoredDiskPortable(c.installation, installedTool, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := c.prepareVerifiedPlan(plan, sums, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedTool, []byte("torn qemu-img"), 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		entries, err := os.ReadDir(filepath.Dir(installedTool))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".dll") {
				if err := os.WriteFile(filepath.Join(filepath.Dir(installedTool), entry.Name()), []byte("torn dll"), 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := os.WriteFile(filepath.Join(c.installation, "guest", "build-spec.json"), []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	activeDisk := filepath.Join(c.installation, "vm", "disk.qcow2")
	f, err := os.OpenFile(activeDisk, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("torn"), 0); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectInstallationDisk(c.installation); err == nil {
		t.Fatal("fixture did not damage the active QCOW2 header")
	}
	recovered, err := c.RecoverPending(nil)
	if err != nil || !recovered {
		t.Fatalf("portable recovery: %v, recovered=%v", err, recovered)
	}
	inventory, err := inspectInstallationDisk(c.installation)
	if err != nil || inventory.Format != "qcow2" || inventory.Backing != "" {
		t.Fatalf("recovered portable disk: %+v, %v", inventory, err)
	}
	output := filepath.Join(t.TempDir(), "recovered.raw")
	if detail, err := exec.Command(tool, "convert", "-f", "qcow2", "-O", "raw", inventory.Path, output).CombinedOutput(); err != nil {
		t.Fatalf("read recovered portable disk: %v: %s", err, detail)
	}
	got, err := os.ReadFile(output)
	if err != nil || len(got) < len(originalRaw) || !bytes.Equal(got[:len(originalRaw)], originalRaw) {
		t.Fatalf("portable disk contents differ: %v", err)
	}
}

func TestManagedGuestUpdateRestoredToolMustBeInsideSnapshot(t *testing.T) {
	restored := t.TempDir()
	name := "qemu-img"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(restored, "runtime", "bin", name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := managedGuestUpdateRestoredTool(restored); err == nil {
		t.Fatal("accepted missing restored tool")
	}
	if err := os.WriteFile(path, []byte("verified snapshot tool"), 0700); err != nil {
		t.Fatal(err)
	}
	got, err := managedGuestUpdateRestoredTool(restored)
	if err != nil || got != path {
		t.Fatalf("selected %s instead of restored tool: %v", got, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(outside, []byte("outside tool"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err == nil {
		if _, err := managedGuestUpdateRestoredTool(restored); err == nil {
			t.Fatal("accepted a tool linked outside the restored snapshot")
		}
	}
}

func TestManagedGuestUpdateRejectsBaselineAndLowSpaceWithoutJournal(t *testing.T) {
	t.Run("wrong baseline", func(t *testing.T) {
		c, plan, sums := managedGuestUpdateFixture(t)
		plan.Baseline.ReleaseManifestSHA256 = strings.Repeat("b", 64)
		if _, err := c.prepareVerifiedPlan(plan, sums, nil); err == nil {
			t.Fatal("accepted an unrelated installed baseline")
		}
		if _, err := os.Lstat(c.journalPath()); !os.IsNotExist(err) {
			t.Fatalf("published journal after baseline failure: %v", err)
		}
	})
	t.Run("insufficient space", func(t *testing.T) {
		c, plan, sums := managedGuestUpdateFixture(t)
		previous := diskFreeBytes
		diskFreeBytes = func(string) (int64, error) { return 0, nil }
		t.Cleanup(func() { diskFreeBytes = previous })
		if _, err := c.prepareVerifiedPlan(plan, sums, nil); !errors.Is(err, errInsufficientDiskSpace) {
			t.Fatalf("low-space checkpoint: %v", err)
		}
		if _, err := os.Lstat(c.journalPath()); !os.IsNotExist(err) {
			t.Fatalf("published journal after failed checkpoint: %v", err)
		}
		entries, err := (checkpointStore{installation: c.installation}).List()
		if err != nil || len(entries) != 0 {
			t.Fatalf("published failed checkpoint: %v %+v", err, entries)
		}
	})
}

func TestManagedGuestUpdateDamagedCheckpointBlocksRecovery(t *testing.T) {
	c, plan, sums := managedGuestUpdateFixture(t)
	journal, err := c.prepareVerifiedPlan(plan, sums, nil)
	if err != nil {
		t.Fatal(err)
	}
	disk := filepath.Join(c.installation, "vm", "disk.raw")
	if err := os.WriteFile(disk, []byte("torn"), 0600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(c.installation, "checkpoints", journal.CheckpointID, "vm.zip")
	f, err := os.OpenFile(archive, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte("bad!"), 16); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := c.RecoverPending(nil); err == nil {
		t.Fatal("accepted a changed checkpoint archive")
	}
	got, _ := os.ReadFile(disk)
	if string(got) != "torn" {
		t.Fatal("changed guest disk after refusing a damaged archive")
	}
	if _, err := os.Lstat(c.journalPath()); err != nil {
		t.Fatalf("removed journal despite failed recovery: %v", err)
	}
}

func TestManagedGuestUpdateHashesBaselineBeyondReceiptTimestamps(t *testing.T) {
	c, plan, sums := managedGuestUpdateFixture(t)
	path := filepath.Join(c.installation, "guest", "vmlinuz-linux")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Repeat([]byte{'x'}, len(before))
	if err := os.WriteFile(path, changed, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	ready, err := installReceiptMatches(filepath.Join(c.installation, "guest"),
		forkReleaseBase+plan.Baseline.Release, plan.Baseline.ReleaseManifestSHA256, installedGuestArtifacts)
	if err != nil || !ready {
		t.Fatalf("fixture did not preserve receipt fast-path metadata: %v, %v", ready, err)
	}
	if _, err := c.prepareVerifiedPlan(plan, sums, nil); err == nil {
		t.Fatal("accepted changed guest bytes with preserved size and timestamp")
	}
	if _, err := os.Lstat(c.journalPath()); !os.IsNotExist(err) {
		t.Fatalf("published journal after content mismatch: %v", err)
	}
}
