package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestMultipleDisplayDeviceIncludesEnabledOutputs(t *testing.T) {
	for _, gpu := range []bool{false, true} {
		cfg := &config{displays: 3, useGpu: gpu, displayWidth: 1440, displayHeight: 900}
		var device struct {
			Driver  string `json:"driver"`
			Outputs []struct {
				Name   string `json:"name"`
				Width  int    `json:"xres"`
				Height int    `json:"yres"`
			} `json:"outputs"`
			Count   int    `json:"max_outputs"`
			HostMem uint64 `json:"hostmem"`
		}
		if err := json.Unmarshal([]byte(displayDevice(cfg, 512<<20)), &device); err != nil {
			t.Fatal(err)
		}
		if device.Count != 3 || len(device.Outputs) != 3 {
			t.Fatal("missing outputs")
		}
		if gpu && device.HostMem != 512<<20 {
			t.Fatal("GPU JSON hostmem must be an integer byte count")
		}
		for i, output := range device.Outputs {
			if output.Width != 1440 || output.Height != 900 || output.Name == "" {
				t.Fatalf("invalid display %d", i)
			}
		}
		if gpu && device.Driver != "virtio-vga-gl" || !gpu && device.Driver != "virtio-gpu-pci" {
			t.Fatal("changed render path")
		}
	}
}

func TestMultipleDisplayDeviceUsesPerMonitorSizes(t *testing.T) {
	cfg := &config{displays: 2, displayWidth: 1920, displayHeight: 1080,
		displaySizes: []guestDisplaySize{{Width: 2560, Height: 1440}, {Width: 3440, Height: 1440}}}
	var device struct {
		Outputs []struct {
			Width  int `json:"xres"`
			Height int `json:"yres"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(displayDevice(cfg, 512<<20)), &device); err != nil {
		t.Fatal(err)
	}
	if len(device.Outputs) != 2 || device.Outputs[0].Width != 2560 || device.Outputs[0].Height != 1440 || device.Outputs[1].Width != 3440 || device.Outputs[1].Height != 1440 {
		t.Fatalf("initial guest output sizes: %+v", device.Outputs)
	}
}

func TestDisplayIdentityAndIndependentPlacements(t *testing.T) {
	index, ok := displayIndexFromTitle("QEMU (" + appTitle + "-2) [Stopped]")
	if !ok || index != 2 {
		t.Fatal("lost console identity")
	}
	for _, title := range []string{appTitle, "QEMU error", "QEMU (" + appTitle + "--1)", "QEMU (" + appTitle + "-16)"} {
		if _, ok := displayIndexFromTitle(title); ok {
			t.Fatalf("accepted %q", title)
		}
	}
	dir := t.TempDir()
	monitors := []screenRect{{0, 0, 1920, 1080}, {1920, 0, 3840, 1080}}
	for index := 0; index < 3; index++ {
		p := initialDisplayPlacement(index, monitors)
		if p == nil || !p.usable(monitors) {
			t.Fatal("unusable placement")
		}
		if err := saveDisplayPlacement(dir, index, *p); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := loadDisplayPlacement(dir, 0)
	second, _ := loadDisplayPlacement(dir, 1)
	if first.Normal == second.Normal {
		t.Fatal("display placements overwrite each other")
	}
	if second.usable(monitors[:1]) {
		t.Fatal("restored placement on a removed monitor")
	}
}

func TestDisplaySettingsValidateAndRestore(t *testing.T) {
	for _, count := range []int{-1, maximumGuestDisplays + 1} {
		if err := (settings{Displays: count}).validate(); err == nil {
			t.Fatal("accepted unsupported display count")
		}
	}
	cfg := &config{}
	var forwards forwardList
	key := ""
	if err := applySettings(cfg, settings{Displays: 3}, map[string]bool{}, &forwards, &key); err != nil || cfg.displays != 3 {
		t.Fatalf("did not apply displays: %v", err)
	}
	cfg.displays = 2
	if err := applySettings(cfg, settings{Displays: 3}, map[string]bool{"displays": true}, &forwards, &key); err != nil || cfg.displays != 2 {
		t.Fatal("ignored explicit display count")
	}
	dir, archive := backupFixture(t)
	placement := windowPlacement{Normal: screenRect{1920, 0, 3200, 900}}
	if err := saveDisplayPlacement(dir, 1, placement); err != nil {
		t.Fatal(err)
	}
	if err := writeVMBackup(dir, archive); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(t.TempDir(), "restored")
	if err := restoreVMBackup(archive, restored); err != nil {
		t.Fatal(err)
	}
	got, err := loadDisplayPlacement(restored, 1)
	if err != nil || got == nil || got.Normal != placement.Normal {
		t.Fatal("backup lost the second display's layout")
	}
}
