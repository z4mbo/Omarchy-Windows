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

// Opt-in integration check: compile the embedded helper, load WinRT, and
// shut down without capturing a user window or changing the display.
func TestSeamlessWGCHelperLifecycle(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_WGC_HELPER") != "1" {
		t.Skip("set OMARCHY_TEST_WGC_HELPER=1 for a local Windows WGC runtime check")
	}
	seamlessWGC.mu.Lock()
	err := seamlessWGC.startLocked()
	seamlessWGC.mu.Unlock()
	defer stopSeamlessWGC()
	if err != nil {
		t.Fatal(err)
	}
}

// Explicit local diagnostic. It only captures the already open Character Map
// HWND, never discovers a substitute target or launches an application.
func TestSeamlessWGCPhysicalCharacterMap(t *testing.T) {
	if os.Getenv("OMARCHY_TEST_WGC_CHARMAP") != "1" {
		t.Skip("set OMARCHY_TEST_WGC_CHARMAP=1 for a local Character Map capture check")
	}
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
	data, err := seamlessWGC.capture(matches[0])
	if err != nil {
		t.Fatalf("WGC capture of Character Map failed: %v", err)
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 {
		t.Fatalf("invalid Character Map PNG: %v", err)
	}
	t.Logf("Character Map WGC frame: %dx%d, %d PNG bytes, pid=%d, hwnd=%s", config.Width, config.Height, len(data), matches[0].PID, matches[0].HWND)
	if seconds, _ := strconv.Atoi(os.Getenv("OMARCHY_TEST_WGC_HOLD_SECONDS")); seconds > 0 && seconds <= 30 {
		t.Logf("holding capture session for %d seconds to inspect Windows capture indicator", seconds)
		time.Sleep(time.Duration(seconds) * time.Second)
	}
}
