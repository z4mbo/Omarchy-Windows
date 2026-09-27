//go:build windows

package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

// This test is explicitly opt-in and never runs in the ordinary test suite.
// It launches only the exact verified experimental QEMU, against a snapshot of
// a separately supplied guest disk. The test changes policy thresholds in
// memory to demonstrate real automatic QMP decisions with at most 768 MiB of
// temporary host pressure. These thresholds are not production settings.
func TestExperimentalBalloonAutomaticPhysicalWindows(t *testing.T) {
	if os.Getenv("OMARCHY_BALLOON_PHYSICAL_TEST") != "1" {
		t.Skip("set OMARCHY_BALLOON_PHYSICAL_TEST=1 for the disposable Windows test")
	}
	need := func(name string) string {
		value := os.Getenv(name)
		if value == "" || !filepath.IsAbs(value) {
			t.Fatalf("%s must be an absolute path", name)
		}
		return filepath.Clean(value)
	}
	qemuPath := need("OMARCHY_BALLOON_QEMU")
	diskPath := need("OMARCHY_BALLOON_DISK")
	kernelPath := need("OMARCHY_BALLOON_KERNEL")
	initrdPath := need("OMARCHY_BALLOON_INITRD")
	sshKey := need("OMARCHY_BALLOON_SSH_KEY")
	workload := need("OMARCHY_BALLOON_WORKLOAD")
	evidenceDir := need("OMARCHY_BALLOON_EVIDENCE_DIR")
	if strings.HasPrefix(strings.ToLower(diskPath), strings.ToLower(`C:\Omarchy\TryOmarchy\`)) {
		t.Fatal("the installed Omarchy disk cannot be used for this snapshot test")
	}
	if _, err := os.Stat(evidenceDir); !os.IsNotExist(err) {
		t.Fatalf("evidence directory must be new: %s", evidenceDir)
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	if digest, err := experimentalRuntimeDigest(qemuPath); err != nil || digest != experimentalBalloonQEMUSHA256 {
		t.Fatalf("experimental QEMU hash is not the pinned, physically verified build: %s, %v", digest, err)
	}
	for _, path := range []string{diskPath, kernelPath, initrdPath, sshKey, workload} {
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("test input is not a regular file: %s, %v", path, err)
		}
	}
	pubkey, err := os.ReadFile(sshKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sshPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	qmpReservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	qmpAddress := qmpReservation.Addr().String()
	qmpReservation.Close()
	serialPath := filepath.Join(evidenceDir, "serial.log")
	stderrPath := filepath.Join(evidenceDir, "qemu-stderr.log")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	args := []string{
		"-machine", "q35,accel=whpx", "-cpu", "host", "-smp", "4", "-m", "4096",
		"-nodefaults", "-display", "none", "-serial", "file:" + serialPath,
		"-device", "virtio-balloon-pci,id=experimental-balloon",
		"-qmp", "tcp:" + qmpAddress + ",server=on,wait=off",
		"-snapshot", "-drive", "file=" + diskPath + ",format=raw,if=virtio",
		"-kernel", kernelPath, "-initrd", initrdPath,
		"-append", "root=/dev/vda rw rootwait console=ttyS0 loglevel=4 tryomarchy.sshd=1 tryomarchy.sshkey=" + base64.StdEncoding.EncodeToString(bytesTrimSpace(pubkey)),
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", sshPort),
		"-device", "virtio-net-pci,netdev=n0",
	}
	cmd := exec.Command(qemuPath, args...)
	cmd.Env = append(os.Environ(), "OMARCHY_QEMU_BALLOON_DECOMMIT=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	var processErr error
	done := make(chan struct{})
	go func() { processErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
		default:
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	ctx, cancelAll := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancelAll()
	// The headless WHPX runtime handles QMP when the monitor connects as QEMU
	// starts. On this host, a first connection made after guest boot accepted
	// TCP but never delivered its greeting. Keep one QMP client for the whole
	// disposable test; qmpClient serializes monitor and controller calls.
	var monitor *qmpClient
	qmpReadyBy := time.Now().Add(30 * time.Second)
	for monitor == nil {
		connection, dialErr := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", qmpAddress)
		if dialErr == nil {
			monitor, err = newQMPClient(ctx, connection)
			if err != nil {
				t.Fatalf("early disposable QMP handshake: %v", err)
			}
			break
		}
		if time.Now().After(qmpReadyBy) {
			t.Fatalf("disposable QMP listener did not start: %v", dialErr)
		}
		time.Sleep(100 * time.Millisecond)
	}
	defer monitor.Close()
	sshArgs := []string{"-p", strconv.Itoa(sshPort), "-i", sshKey, "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=NUL",
		"-o", "ConnectTimeout=3", "z4mbo@127.0.0.1"}
	ssh := func(remote string) (string, error) {
		childCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		command := exec.CommandContext(childCtx, "ssh.exe", append(sshArgs, remote)...)
		var stderr strings.Builder
		command.Stderr = &stderr
		out, err := command.Output()
		if err != nil {
			return string(out) + stderr.String(), err
		}
		return string(out), nil
	}
	readyBy := time.Now().Add(100 * time.Second)
	for {
		if out, err := ssh("echo ready"); err == nil && strings.Contains(out, "ready") {
			break
		}
		select {
		case <-done:
			t.Fatalf("disposable QEMU exited before guest SSH: %v", processErr)
		default:
		}
		if time.Now().After(readyBy) {
			t.Fatal("disposable guest did not enable SSH")
		}
		time.Sleep(2 * time.Second)
	}
	scpArgs := []string{"-P", strconv.Itoa(sshPort), "-i", sshKey, "-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no", "-o", "UserKnownHostsFile=NUL",
		workload, "z4mbo@127.0.0.1:/tmp/omarchy-balloon-workload.py"}
	if out, err := exec.CommandContext(ctx, "scp.exe", scpArgs...).CombinedOutput(); err != nil {
		t.Fatalf("copying integrity workload: %v: %s", err, out)
	}
	if out, err := ssh("nohup python /tmp/omarchy-balloon-workload.py >/tmp/omarchy-balloon-workload.out 2>&1 </dev/null &"); err != nil {
		t.Fatalf("starting integrity workload: %v: %s", err, out)
	}
	// The random loopback monitor is test-only. The installed launcher keeps
	// its private Unix sockets and never starts this controller.
	balloonActual := func() int64 {
		var result struct {
			Actual int64 `json:"actual"`
		}
		if err := monitor.Call(ctx, "query-balloon", nil, &result); err != nil {
			t.Fatal(err)
		}
		return result.Actual
	}
	if actual := balloonActual(); actual != 4096<<20 {
		t.Fatalf("initial balloon actual = %d", actual)
	}
	time.Sleep(10 * time.Second)
	total, baseline := availMemMiB()
	if total <= 0 || baseline < 8192 {
		t.Fatalf("insufficient Windows memory for bounded test: total=%d available=%d MiB", total, baseline)
	}
	workingBefore, err := physicalQEMUWorkingMiB(pid)
	if err != nil {
		t.Fatal(err)
	}
	pressure, err := physicalBalloonAllocatePressure(768 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer pressure.release()
	_, pressured := availMemMiB()
	if baseline-pressured < 256 {
		t.Fatalf("bounded host allocation did not lower available memory enough: %d -> %d MiB", baseline, pressured)
	}
	policy, err := newExperimentalBalloonPolicy(4096, 3584, total)
	if err != nil {
		t.Fatal(err)
	}
	productionHeadroom := policy.headroomMiB
	policy.headroomMiB = (baseline + pressured) / 2 // test-only threshold injection
	result := map[string]any{
		"verified_qemu_sha256": experimentalBalloonQEMUSHA256,
		"qemu_pid":             pid, "pressure_mib": 768,
		"production_headroom_mib":  productionHeadroom,
		"test_floor_mib":           3584,
		"test_shrink_headroom_mib": policy.headroomMiB,
		"host_before_mib":          baseline,
		"host_under_pressure_mib":  pressured,
		"qemu_working_before_mib":  workingBefore,
	}
	defer func() {
		data, _ := json.MarshalIndent(result, "", "  ")
		_ = os.WriteFile(filepath.Join(evidenceDir, "result.json"), append(data, '\n'), 0600)
	}()
	stillRunning := func() bool {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
	var growthPhase atomic.Bool
	growthThreshold := make(chan int, 1)
	growthAdjusted := false // accessed only by controller.run through measure
	measure := func() (int, error) {
		_, available := availMemMiB()
		if available <= 0 {
			return 0, fmt.Errorf("Windows available memory unavailable")
		}
		if growthPhase.Load() && !growthAdjusted {
			policy.headroomMiB = available - 4096 // test-only growth threshold injection
			growthAdjusted = true
			growthThreshold <- policy.headroomMiB
		}
		return available, nil
	}
	path := "/machine/peripheral/experimental-balloon"
	controlCtx, cancelController := context.WithCancel(ctx)
	defer cancelController()
	controller := &experimentalBalloonController{
		policy: policy, qmp: monitor, balloonPath: path,
		measureAvailableMiB: measure, stillRunning: stillRunning, now: time.Now,
	}
	controllerDone := make(chan error, 1)
	go func() { controllerDone <- controller.run(controlCtx) }()
	waitActual := func(want int64, timeout time.Duration) {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if actual := balloonActual(); actual == want {
				return
			}
			select {
			case err := <-controllerDone:
				t.Fatalf("automatic balloon controller stopped: %v", err)
			default:
			}
			time.Sleep(time.Second)
		}
		t.Fatalf("automatic balloon target %d not reached; actual=%d", want, balloonActual())
	}
	waitActual(3584<<20, 75*time.Second)
	_, afterShrink := availMemMiB()
	workingAfterShrink, err := physicalQEMUWorkingMiB(pid)
	if err != nil {
		t.Fatal(err)
	}
	result["host_after_automatic_shrink_mib"] = afterShrink
	result["qemu_working_after_shrink_mib"] = workingAfterShrink
	result["automatic_shrink_actual_bytes"] = balloonActual()
	t.Logf("automatic shrink: Windows available %d -> %d -> %d MiB; QEMU working %.1f -> %.1f MiB",
		baseline, pressured, afterShrink, workingBefore, workingAfterShrink)
	if err := pressure.release(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Second)
	_, released := availMemMiB()
	result["host_after_pressure_release_mib"] = released
	growthPhase.Store(true)
	waitActual(4096<<20, 190*time.Second)
	result["test_growth_headroom_mib"] = <-growthThreshold
	_, afterGrow := availMemMiB()
	workingAfterGrow, err := physicalQEMUWorkingMiB(pid)
	if err != nil {
		t.Fatal(err)
	}
	result["host_after_automatic_grow_mib"] = afterGrow
	result["qemu_working_after_grow_mib"] = workingAfterGrow
	result["automatic_grow_actual_bytes"] = balloonActual()
	t.Logf("automatic grow: Windows available %d -> %d MiB; QEMU working %.1f MiB",
		released, afterGrow, workingAfterGrow)
	// The guest workload writes every round as it runs. Check all completed
	// rounds rather than only its final line, so corruption cannot be hidden.
	integrityDeadline := time.Now().Add(150 * time.Second)
	for {
		output, err := ssh("cat /tmp/omarchy-balloon-integrity.log")
		if err == nil {
			if writeErr := os.WriteFile(filepath.Join(evidenceDir, "guest-integrity-raw.log"), []byte(output), 0600); writeErr != nil {
				t.Fatal(writeErr)
			}
			good, complete := physicalBalloonIntegrity(output)
			if !good {
				t.Fatalf("guest memory integrity output invalid; inspect guest-integrity-raw.log in %s", evidenceDir)
			}
			if complete {
				result["integrity_rounds"] = 120
				break
			}
		}
		if time.Now().After(integrityDeadline) {
			t.Fatal("guest integrity workload did not finish")
		}
		time.Sleep(5 * time.Second)
	}
	if err := monitor.Call(ctx, "system_powerdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("disposable QEMU shutdown: %v", processErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("disposable QEMU did not shut down")
	}
	cancelController()
	result["clean_shutdown"] = true
	t.Logf("automatic controller physical test passed; evidence: %s", evidenceDir)
}

func bytesTrimSpace(data []byte) []byte { return []byte(strings.TrimSpace(string(data))) }

type physicalBalloonPressure struct{ address uintptr }

func physicalBalloonAllocatePressure(size uintptr) (*physicalBalloonPressure, error) {
	allocate := kernel32.NewProc("VirtualAlloc")
	address, _, err := allocate.Call(0, size, 0x3000, 0x04) // MEM_RESERVE|MEM_COMMIT, PAGE_READWRITE
	if address == 0 {
		return nil, fmt.Errorf("VirtualAlloc pressure: %w", err)
	}
	for offset := uintptr(0); offset < size; offset += 4096 {
		*(*byte)(unsafe.Pointer(address + offset)) = byte(offset >> 12)
	}
	return &physicalBalloonPressure{address: address}, nil
}

func (p *physicalBalloonPressure) release() error {
	if p == nil || p.address == 0 {
		return nil
	}
	release := kernel32.NewProc("VirtualFree")
	r, _, err := release.Call(p.address, 0, 0x8000) // MEM_RELEASE
	if r == 0 {
		return fmt.Errorf("VirtualFree pressure: %w", err)
	}
	p.address = 0
	return nil
}

func physicalQEMUWorkingMiB(pid int) (float64, error) {
	out, err := exec.Command("powershell.exe", "-NoProfile", "-Command",
		fmt.Sprintf("(Get-Process -Id %d).WorkingSet64", pid)).Output()
	if err != nil {
		return 0, err
	}
	bytes, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return float64(bytes) / (1 << 20), err
}

func physicalBalloonIntegrity(log string) (good, complete bool) {
	scan := bufio.NewScanner(strings.NewReader(log))
	count := 0
	for scan.Scan() {
		var row struct {
			Round int  `json:"round"`
			Okay  bool `json:"okay"`
		}
		if json.Unmarshal(scan.Bytes(), &row) != nil || !row.Okay || row.Round != count {
			return false, false
		}
		count++
	}
	return scan.Err() == nil && count <= 120, count == 120
}
