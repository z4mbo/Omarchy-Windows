//go:build windows

package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestSeamlessWSD1FullFrameDecodesToPNG(t *testing.T) {
	frame := make([]byte, 36+8)
	copy(frame[:4], "WSD1")
	for offset, value := range map[int]uint32{4: 2, 8: 1, 12: 8, 16: 1, 20: 0, 24: 0, 28: 2, 32: 1} {
		binary.LittleEndian.PutUint32(frame[offset:offset+4], value)
	}
	copy(frame[36:], []byte{0, 0, 255, 255, 0, 255, 0, 255}) // red, green BGRA
	encoded, err := seamlessWSD1ToPNG(frame)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if r != 0xffff || g != 0 || b != 0 {
		t.Fatalf("first pixel: %04x %04x %04x", r, g, b)
	}
	r, g, b, _ = decoded.At(1, 0).RGBA()
	if r != 0 || g != 0xffff || b != 0 {
		t.Fatalf("second pixel: %04x %04x %04x", r, g, b)
	}
	// A delta must not be rendered as a complete frame from another HWND.
	binary.LittleEndian.PutUint32(frame[28:32], 1)
	if _, err := seamlessWSD1ToPNG(frame); err == nil {
		t.Fatal("partial frame was accepted")
	}
}

func TestSeamlessWGCOptInDefaultsOff(t *testing.T) {
	t.Setenv(seamlessWGCEnv, "")
	if seamlessWGCRequested() {
		t.Fatal("capture started without opt-in")
	}
	t.Setenv(seamlessWGCEnv, "1")
	if !seamlessWGCRequested() {
		t.Fatal("explicit opt-in was ignored")
	}
}

func TestSeamlessWGCFramesFallBackWhilePreparingOrBusy(t *testing.T) {
	c := &seamlessWGCClient{}
	c.prewarming.Store(true)
	start := time.Now()
	if _, err := c.capture(seamlessWindow{}); err == nil {
		t.Fatal("capture did not fall back during helper preparation")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("capture waited for helper preparation")
	}
	c.prewarming.Store(false)
	c.mu.Lock()
	start = time.Now()
	if _, err := c.capture(seamlessWindow{}); err == nil {
		t.Fatal("capture did not fall back while another frame was in progress")
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("capture waited for the busy helper")
	}
	c.mu.Unlock()
}

// Opt-in integration check: compile the embedded helper, load WinRT, and
// shut down without capturing a user window or changing the display.
func TestSeamlessWGCHelperLifecycle(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_WGC_HELPER") != "1" {
		t.Skip("set OMARCHY_TEST_WGC_HELPER=1 for a local Windows WGC runtime check")
	}
	t.Setenv(seamlessWGCEnv, "1")
	prewarmSeamlessWGC()
	deadline := time.Now().Add(40 * time.Second)
	for seamlessWGC.prewarming.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	defer stopSeamlessWGC()
	seamlessWGC.mu.Lock()
	defer seamlessWGC.mu.Unlock()
	if seamlessWGC.prewarming.Load() || seamlessWGC.disabled || seamlessWGC.cmd == nil {
		t.Fatal("WGC helper did not finish prewarming")
	}
}

// Explicit local diagnostic. It only captures the already open Character Map
// HWND, never discovers a substitute target or launches an application.
func TestSeamlessWGCPhysicalCharacterMap(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_WGC_CHARMAP") != "1" {
		t.Skip("set OMARCHY_TEST_WGC_CHARMAP=1 for a local Character Map capture check")
	}
	t.Setenv(seamlessWGCEnv, "1")
	var matches []seamlessWindow
	for _, window := range (nativeSeamlessWindows{}).windows() {
		if strings.EqualFold(window.Process, "charmap.exe") && window.Title == "Character Map" {
			matches = append(matches, window)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("expected exactly one already open Character Map window; found %d", len(matches))
	}
	t.Logf("target pid=%d hwnd=%s size=%dx%d", matches[0].PID, matches[0].HWND, matches[0].Width, matches[0].Height)
	defer stopSeamlessWGC()
	prewarmSeamlessWGC()
	deadline := time.Now().Add(10 * time.Second)
	for seamlessWGC.prewarming.Load() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if seamlessWGC.prewarming.Load() {
		t.Fatal("WGC helper did not finish preparing")
	}
	data, err := seamlessWGC.capture(matches[0])
	if err != nil {
		t.Fatalf("WGC capture of Character Map failed: %v", err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 {
		t.Fatalf("invalid Character Map PNG: %v", err)
	}
	t.Logf("Character Map WGC frame: %dx%d, %d PNG bytes, pid=%d, hwnd=%s", config.Width, config.Height, len(data), matches[0].PID, matches[0].HWND)
	var outer, visible seamlessRect
	if ok, _, _ := seamlessGetRect.Call(matches[0].handle, uintptr(unsafe.Pointer(&outer))); ok == 0 || !seamlessVisibleRect(matches[0].handle, &visible) {
		t.Fatal("could not read Character Map window bounds")
	}
	aligned, err := seamlessAlignWGCFrame(data, outer, visible)
	if err != nil {
		t.Fatalf("WGC frame could not be mapped to window coordinates: %v", err)
	}
	alignedConfig, err := png.DecodeConfig(bytes.NewReader(aligned))
	if err != nil || alignedConfig.Width != int(outer.right-outer.left) || alignedConfig.Height != int(outer.bottom-outer.top) {
		t.Fatalf("aligned frame does not match window bounds: %dx%d, %v", alignedConfig.Width, alignedConfig.Height, err)
	}
	if seconds, _ := strconv.Atoi(os.Getenv("OMARCHY_TEST_WGC_HOLD_SECONDS")); seconds > 0 && seconds <= 30 {
		t.Logf("holding capture session for %d seconds to inspect Windows capture indicator", seconds)
		time.Sleep(time.Duration(seconds) * time.Second)
	}
}
