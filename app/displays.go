package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const maximumGuestDisplays = 16

type guestDisplaySize struct{ Width, Height int }

func guestDisplayCount(count int) int {
	if count == 0 {
		return 1
	}
	return count
}

func displayDevice(cfg *config, hostmem uint64) string {
	if guestDisplayCount(cfg.displays) == 1 {
		if cfg.useGpu {
			return "virtio-vga-gl,blob=on,hostmem=" + strconv.FormatUint(hostmem, 10) + ",venus=on"
		}
		return "virtio-gpu-pci,id=gpu0"
	}
	width, height := cfg.displayWidth, cfg.displayHeight
	if width < 640 || width > 8192 {
		width = 1920
	}
	if height < 480 || height > 8192 {
		height = 1080
	}
	outputs := make([]map[string]any, guestDisplayCount(cfg.displays))
	for i := range outputs {
		outputWidth, outputHeight := width, height
		if i < len(cfg.displaySizes) {
			if candidate := cfg.displaySizes[i]; candidate.Width >= 640 && candidate.Width <= 8192 && candidate.Height >= 480 && candidate.Height <= 8192 {
				outputWidth, outputHeight = candidate.Width, candidate.Height
			}
		}
		outputs[i] = map[string]any{"name": fmt.Sprintf("Omarchy %d", i+1), "xres": outputWidth, "yres": outputHeight}
	}
	device := map[string]any{"driver": "virtio-gpu-pci", "id": "gpu0", "max_outputs": len(outputs), "outputs": outputs}
	if cfg.useGpu {
		device["driver"] = "virtio-vga-gl"
		device["blob"] = true
		device["hostmem"] = hostmem
		device["venus"] = true
	}
	data, _ := json.Marshal(device)
	return string(data)
}

func displayIndexFromTitle(title string) (int, bool) {
	prefix := "QEMU (" + appTitle + "-"
	if !strings.HasPrefix(title, prefix) {
		return 0, false
	}
	tail := strings.TrimPrefix(title, prefix)
	end := strings.IndexByte(tail, ')')
	if end < 1 {
		return 0, false
	}
	index, err := strconv.Atoi(tail[:end])
	return index, err == nil && index >= 0 && index < maximumGuestDisplays
}

func displayPlacementFilename(index int) string {
	if index == 0 {
		return windowPlacementFilename
	}
	return fmt.Sprintf("window-placement-display-%d.json", index+1)
}

func initialDisplayPlacement(index int, monitors []screenRect) *windowPlacement {
	if len(monitors) == 0 {
		return nil
	}
	monitor := monitors[index%len(monitors)]
	if index < len(monitors) {
		return &windowPlacement{Normal: screenRect{monitor.Left + 24, monitor.Top + 24, monitor.Right - 24, monitor.Bottom - 24}, Maximized: true}
	}
	// Extra virtual displays on one physical monitor remain independently usable.
	offset := int32(24 * (1 + index%5))
	return &windowPlacement{Normal: screenRect{monitor.Left + offset, monitor.Top + offset, monitor.Right - 24, monitor.Bottom - 24}}
}
