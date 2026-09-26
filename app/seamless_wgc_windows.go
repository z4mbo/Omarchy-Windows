//go:build windows

package main

import (
	"bytes"
	"context"
	"embed"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The captured WGC implementation is MIT licensed. The full notice is
// embedded alongside its source and staged with the opt-in helper.
//
//go:embed capture/wayseam_wgc.cs capture/omarchy_wgc_host.cs capture/WAYSEAM-LICENSE.txt capture/NOTICE.md
var seamlessWGCSources embed.FS

const seamlessWGCEnv = "OMARCHY_SEAMLESS_WGC"
const seamlessWGCMaximumBytes = 60_000_000
const seamlessWGCFrameTimeout = 4 * time.Second

var seamlessWGC = &seamlessWGCClient{}

type seamlessWGCClient struct {
	mu         sync.Mutex
	prewarming atomic.Bool
	disabled   bool
	workDir    string
	helperPath string
	cmd        *exec.Cmd
	stdin      io.WriteCloser
	stdout     io.ReadCloser
}

// WGC is intentionally opt-in while the host and guest share one Windows
// interactive session. It can capture GPU-rendered borderless HWNDs, but the
// PNG-over-HTTP presenter is not a game streaming transport and game input
// remains unverified.
func seamlessWGCRequested() bool {
	return os.Getenv(seamlessWGCEnv) == "1"
}

func seamlessWGCFrame(window seamlessWindow) ([]byte, error) {
	if !seamlessWGCRequested() {
		return nil, errSeamlessCapture
	}
	return seamlessWGC.capture(window)
}

// Prepare the optional helper before the guest can request its first frame.
// While it compiles and starts, frame requests use the PrintWindow fallback.
func prewarmSeamlessWGC() {
	if !seamlessWGCRequested() || !seamlessWGC.prewarming.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer seamlessWGC.prewarming.Store(false)
		seamlessWGC.mu.Lock()
		defer seamlessWGC.mu.Unlock()
		if err := seamlessWGC.startLocked(); err != nil {
			seamlessWGC.disabled = true
			logf("optional Windows Graphics Capture unavailable: %v", err)
		}
	}()
}

func stopSeamlessWGC() {
	seamlessWGC.mu.Lock()
	defer seamlessWGC.mu.Unlock()
	seamlessWGC.stopLocked()
	if seamlessWGC.workDir != "" {
		for _, name := range []string{"omarchy-wgc-helper.exe", "wayseam_wgc.cs", "omarchy_wgc_host.cs", "WAYSEAM-LICENSE.txt", "NOTICE.md"} {
			_ = os.Remove(filepath.Join(seamlessWGC.workDir, name))
		}
		_ = os.Remove(seamlessWGC.workDir)
		seamlessWGC.workDir = ""
		seamlessWGC.helperPath = ""
	}
}

func (c *seamlessWGCClient) capture(window seamlessWindow) ([]byte, error) {
	if c.prewarming.Load() || !c.mu.TryLock() {
		return nil, errSeamlessCapture
	}
	defer c.mu.Unlock()
	if c.disabled {
		return nil, errSeamlessCapture
	}
	if err := c.startLocked(); err != nil {
		c.disabled = true
		logf("optional Windows Graphics Capture unavailable: %v", err)
		return nil, errSeamlessCapture
	}
	stdin, stdout := c.stdin, c.stdout
	result := make(chan struct {
		frame []byte
		err   error
	}, 1)
	go func() {
		if _, err := io.WriteString(stdin, "capture "+window.HWND+"\n"); err != nil {
			result <- struct {
				frame []byte
				err   error
			}{nil, err}
			return
		}
		var length int32
		if err := binary.Read(stdout, binary.LittleEndian, &length); err != nil {
			result <- struct {
				frame []byte
				err   error
			}{nil, err}
			return
		}
		if length < 36 || length > seamlessWGCMaximumBytes {
			result <- struct {
				frame []byte
				err   error
			}{nil, errSeamlessCapture}
			return
		}
		payload := make([]byte, int(length))
		_, err := io.ReadFull(stdout, payload)
		result <- struct {
			frame []byte
			err   error
		}{payload, err}
	}()
	select {
	case captured := <-result:
		if captured.err != nil {
			if !errors.Is(captured.err, errSeamlessCapture) {
				c.stopLocked()
			}
			return nil, errSeamlessCapture
		}
		return seamlessWSD1ToPNG(captured.frame)
	case <-time.After(seamlessWGCFrameTimeout):
		c.stopLocked()
		return nil, errSeamlessCapture
	}
}

func (c *seamlessWGCClient) startLocked() error {
	if c.cmd != nil {
		return nil
	}
	if c.helperPath == "" {
		if err := c.compileLocked(); err != nil {
			return err
		}
	}
	cmd := exec.Command(c.helperPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return err
	}
	ready := make(chan error, 1)
	go func() {
		var greeting [5]byte
		_, err := io.ReadFull(stdout, greeting[:])
		if err == nil && (string(greeting[:4]) != "OWGC" || greeting[4] != 1) {
			err = errors.New("WGC unavailable on this Windows session")
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return err
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return errors.New("WGC helper did not start")
	}
	c.cmd, c.stdin, c.stdout = cmd, stdin, stdout
	return nil
}

func (c *seamlessWGCClient) stopLocked() {
	if c.cmd == nil {
		return
	}
	_ = c.cmd.Process.Kill()
	_ = c.cmd.Wait()
	c.cmd = nil
	c.stdin = nil
	c.stdout = nil
}

func (c *seamlessWGCClient) compileLocked() error {
	compiler := filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "Framework64", "v4.0.30319", "csc.exe")
	if info, err := os.Stat(compiler); err != nil || info.IsDir() {
		return errors.New("Windows .NET Framework compiler unavailable")
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	root := filepath.Join(cacheDir, "Omarchy", "seamless")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	workDir, err := os.MkdirTemp(root, "wgc-*")
	if err != nil {
		return err
	}
	c.workDir = workDir
	var sources []string
	for _, asset := range []string{"capture/wayseam_wgc.cs", "capture/omarchy_wgc_host.cs", "capture/WAYSEAM-LICENSE.txt", "capture/NOTICE.md"} {
		content, err := seamlessWGCSources.ReadFile(asset)
		if err != nil {
			return err
		}
		path := filepath.Join(workDir, filepath.Base(asset))
		if err := os.WriteFile(path, content, 0600); err != nil {
			return err
		}
		if err := restrictSeamlessTokenWindows(path); err != nil {
			return err
		}
		if strings.HasSuffix(asset, ".cs") {
			sources = append(sources, path)
		}
	}
	// Names are internal, randomly staged, and never derived from an HWND or
	// guest input. System WinRT metadata is supplied by Windows itself.
	helper := filepath.Join(workDir, "omarchy-wgc-helper.exe")
	framework := filepath.Dir(compiler)
	refs := []string{
		filepath.Join(framework, "System.Runtime.WindowsRuntime.dll"),
		filepath.Join(framework, "System.Runtime.InteropServices.WindowsRuntime.dll"),
		filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "assembly", "GAC_MSIL", "System.Runtime", "v4.0_4.0.0.0__b03f5f7f11d50a3a", "System.Runtime.dll"),
		filepath.Join(os.Getenv("WINDIR"), "System32", "WinMetadata", "Windows.Foundation.winmd"),
		filepath.Join(os.Getenv("WINDIR"), "System32", "WinMetadata", "Windows.Graphics.winmd"),
	}
	args := []string{"/nologo", "/target:exe", "/optimize+", "/out:" + helper}
	for _, ref := range refs {
		args = append(args, "/r:"+ref)
	}
	args = append(args, sources...)
	buildContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	build := exec.CommandContext(buildContext, compiler, args...)
	build.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if output, err := build.CombinedOutput(); err != nil {
		if buildContext.Err() != nil {
			return fmt.Errorf("compile WGC helper: %w", buildContext.Err())
		}
		message := string(output)
		if len(message) > 512 {
			message = message[:512]
		}
		return fmt.Errorf("compile WGC helper: %w: %s", err, strings.TrimSpace(message))
	}
	if err := restrictSeamlessTokenWindows(helper); err != nil {
		return err
	}
	c.helperPath = helper
	return nil
}

// WSD1 is Wayseam's little-endian full-frame BGRA envelope. The sidecar
// always asks for a full frame (base sequence -1); reject deltas or malformed
// geometry rather than mixing pixels from different Windows windows.
func seamlessWSD1ToPNG(frame []byte) ([]byte, error) {
	if len(frame) < 36 || string(frame[:4]) != "WSD1" {
		return nil, errSeamlessCapture
	}
	width := int(int32(binary.LittleEndian.Uint32(frame[4:8])))
	height := int(int32(binary.LittleEndian.Uint32(frame[8:12])))
	stride := int(int32(binary.LittleEndian.Uint32(frame[12:16])))
	x := int(int32(binary.LittleEndian.Uint32(frame[20:24])))
	y := int(int32(binary.LittleEndian.Uint32(frame[24:28])))
	changedWidth := int(int32(binary.LittleEndian.Uint32(frame[28:32])))
	changedHeight := int(int32(binary.LittleEndian.Uint32(frame[32:36])))
	if width < 1 || height < 1 || width > 4096 || height > 4096 ||
		int64(width)*int64(height) > 12_000_000 || stride != width*4 ||
		x != 0 || y != 0 || changedWidth != width || changedHeight != height ||
		len(frame) != 36+width*height*4 {
		return nil, errSeamlessCapture
	}
	bitmap := image.NewRGBA(image.Rect(0, 0, width, height))
	for i, source := 0, 36; i < len(bitmap.Pix); i, source = i+4, source+4 {
		bitmap.Pix[i] = frame[source+2]
		bitmap.Pix[i+1] = frame[source+1]
		bitmap.Pix[i+2] = frame[source]
		bitmap.Pix[i+3] = 255
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, bitmap); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
