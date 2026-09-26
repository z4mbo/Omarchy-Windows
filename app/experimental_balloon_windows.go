//go:build windows

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Only this physically tested, separately built runtime may be used by the
// experimental controller. Rebuilds need their own validation and hash change.
const experimentalBalloonQEMUSHA256 = "43160f86cbf28a67df6f529b104dd03f63a37ca5d3d2a9148f11358fbc559b0a"

type experimentalBalloonOptions struct {
	QEMUPath          string
	QEMUPID           int
	QMPSocket         string
	BalloonPath       string
	BootMiB, FloorMiB int
}

var procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")

func experimentalProcessImage(handle syscall.Handle) (string, error) {
	buffer := make([]uint16, 32768)
	n := uint32(len(buffer))
	r, _, err := procQueryFullProcessImageNameW.Call(uintptr(handle), 0,
		uintptr(unsafe.Pointer(&buffer[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 || n == 0 || n >= uint32(len(buffer)) {
		return "", fmt.Errorf("querying experimental QEMU process image: %w", err)
	}
	return syscall.UTF16ToString(buffer[:n]), nil
}

func experimentalRuntimeDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("experimental QEMU image is not a regular file")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func experimentalImagePath(path string) string {
	path = strings.TrimPrefix(path, `\\?\`)
	return filepath.Clean(path)
}

// runExperimentalBalloonOnWindows is an explicit opt-in entry point; normal
// launch never invokes it. It requires the exact experimental executable, the
// live PID of that image, and a private socket named for that PID. The caller
// must create the socket in the test QEMU invocation; no production control
// socket is accepted. The controller additionally requires the experimental
// QOM property to report that the running process enabled RAM reclaim.
func runExperimentalBalloonOnWindows(ctx context.Context, o experimentalBalloonOptions) error {
	if o.QEMUPID <= 0 || !filepath.IsAbs(o.QEMUPath) ||
		!strings.EqualFold(filepath.Base(o.QEMUPath), "qemu-system-x86_64w.exe") ||
		!filepath.IsAbs(o.QMPSocket) ||
		filepath.Base(o.QMPSocket) != "experimental-balloon-"+strconv.Itoa(o.QEMUPID)+".sock" ||
		!strings.HasPrefix(o.BalloonPath, "/machine/peripheral/") ||
		strings.Contains(strings.TrimPrefix(o.BalloonPath, "/machine/peripheral/"), "/") ||
		len(o.BalloonPath) <= len("/machine/peripheral/") {
		return fmt.Errorf("invalid experimental balloon connection options")
	}
	handle, err := syscall.OpenProcess(0x1000|0x100000, false, uint32(o.QEMUPID)) // QUERY_LIMITED_INFORMATION | SYNCHRONIZE
	if err != nil {
		return fmt.Errorf("opening experimental QEMU process: %w", err)
	}
	defer syscall.CloseHandle(handle)
	image, err := experimentalProcessImage(handle)
	if err != nil {
		return err
	}
	if !strings.EqualFold(experimentalImagePath(image), experimentalImagePath(o.QEMUPath)) {
		return fmt.Errorf("experimental QEMU path does not match the running process")
	}
	digest, err := experimentalRuntimeDigest(o.QEMUPath)
	if err != nil {
		return err
	}
	if digest != experimentalBalloonQEMUSHA256 {
		return fmt.Errorf("QEMU runtime is not the verified experimental balloon build")
	}
	stillRunning := func() bool {
		state, err := syscall.WaitForSingleObject(handle, 0)
		return err == nil && state == 258 // WAIT_TIMEOUT
	}
	if !stillRunning() {
		return fmt.Errorf("experimental QEMU process exited")
	}
	total, available := availMemMiB()
	if total <= 0 || available <= 0 {
		return fmt.Errorf("Windows memory sample unavailable")
	}
	policy, err := newExperimentalBalloonPolicy(o.BootMiB, o.FloorMiB, total)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", o.QMPSocket)
	if err != nil {
		return fmt.Errorf("dialing experimental QMP socket: %w", err)
	}
	qmp, err := newQMPClient(ctx, conn)
	if err != nil {
		return err
	}
	defer qmp.Close()
	c := experimentalBalloonController{
		policy: policy, qmp: qmp, balloonPath: o.BalloonPath,
		measureAvailableMiB: func() (int, error) {
			_, available := availMemMiB()
			if available <= 0 {
				return 0, fmt.Errorf("Windows memory sample unavailable")
			}
			return available, nil
		},
		stillRunning: stillRunning, now: time.Now,
	}
	return c.run(ctx)
}
