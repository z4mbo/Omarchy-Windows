//go:build windows

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// This opt-in test uses only a verified experimental QEMU and an inactive
// acceptance disk with -snapshot. It tests the actual Windows adapter and
// four independent private AF_UNIX monitors, without host memory pressure.
func TestExperimentalBalloonAdapterPhysicalWindows(t *testing.T) {
	if os.Getenv("OMARCHY_BALLOON_ADAPTER_PHYSICAL_TEST") != "1" {
		t.Skip("set OMARCHY_BALLOON_ADAPTER_PHYSICAL_TEST=1 for the disposable adapter test")
	}
	need := func(name string) string {
		path := os.Getenv(name)
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must be an absolute path", name)
		}
		path = filepath.Clean(path)
		if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("%s must name a regular file: %v", name, err)
		}
		return path
	}
	qemuPath := need("OMARCHY_BALLOON_QEMU")
	diskPath := need("OMARCHY_BALLOON_DISK")
	kernelPath := need("OMARCHY_BALLOON_KERNEL")
	initrdPath := need("OMARCHY_BALLOON_INITRD")
	sshKey := need("OMARCHY_BALLOON_SSH_KEY")
	if !strings.Contains(strings.ToLower(diskPath), strings.ToLower(`\dist\acceptance\vm\disk.raw`)) {
		t.Fatal("this test accepts only the inactive acceptance disk")
	}
	evidenceDir := os.Getenv("OMARCHY_BALLOON_EVIDENCE_DIR")
	if !filepath.IsAbs(evidenceDir) {
		t.Fatal("OMARCHY_BALLOON_EVIDENCE_DIR must be an absolute path")
	}
	if _, err := os.Stat(evidenceDir); !os.IsNotExist(err) {
		t.Fatal("evidence directory must be new")
	}
	if digest, err := experimentalRuntimeDigest(qemuPath); err != nil || digest != experimentalBalloonQEMUSHA256 {
		t.Fatalf("QEMU is not the pinned experimental build: %s, %v", digest, err)
	}
	if err := os.MkdirAll(evidenceDir, 0700); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"verified_qemu_sha256": experimentalBalloonQEMUSHA256, "snapshot": true,
		"normal_launcher_used": false}
	defer func() {
		result["test_failed"] = t.Failed()
		data, _ := json.MarshalIndent(result, "", "  ")
		_ = os.WriteFile(filepath.Join(evidenceDir, "result.json"), append(data, '\n'), 0600)
	}()
	control, err := prepareExperimentalBalloonControl()
	if err != nil {
		t.Fatal(err)
	}
	result["private_control_directory"] = control.dir
	otherPaths := []string{
		filepath.Join(control.dir, "supervisor.sock"),
		filepath.Join(control.dir, "tools.sock"),
		filepath.Join(control.dir, "forward.sock"),
	}
	var monitors []*qmpClient
	var cmd *exec.Cmd
	var done chan struct{}
	var processErr error
	defer func() {
		for _, monitor := range monitors {
			_ = monitor.Close()
		}
		if cmd != nil && done != nil {
			select {
			case <-done:
			default:
				_ = cmd.Process.Kill() // only the disposable process started here
				<-done
			}
		}
		for _, path := range otherPaths {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Errorf("remove disposable monitor %s: %v", filepath.Base(path), err)
			}
		}
		if err := control.Close(); err != nil {
			t.Errorf("close disposable balloon control: %v", err)
		}
	}()
	pubkey, err := os.ReadFile(sshKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sshPort := reservation.Addr().(*net.TCPAddr).Port
	reservation.Close()
	stderr, err := os.Create(filepath.Join(evidenceDir, "qemu-stderr.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	args := []string{
		"-machine", "q35,accel=whpx", "-cpu", "host", "-smp", "4", "-m", "4096",
		"-nodefaults", "-no-reboot", "-display", "none",
		"-serial", "file:" + filepath.Join(evidenceDir, "serial.log"),
		"-device", "virtio-balloon-pci,id=experimental-balloon",
		"-snapshot", "-drive", "file=" + qemuOptionValue(diskPath) + ",format=raw,if=virtio",
		"-kernel", kernelPath, "-initrd", initrdPath,
		"-append", "root=/dev/vda rw rootwait console=ttyS0 loglevel=4 tryomarchy.sshd=1 tryomarchy.sshkey=" + base64.StdEncoding.EncodeToString([]byte(strings.TrimSpace(string(pubkey)))),
		"-netdev", fmt.Sprintf("user,id=n0,hostfwd=tcp:127.0.0.1:%d-:22", sshPort),
		"-device", "virtio-net-pci,netdev=n0",
		"-qmp", "unix:" + qemuOptionValue(control.path) + ",server=on,wait=off",
	}
	for _, path := range otherPaths {
		args = append(args, "-qmp", "unix:"+qemuOptionValue(path)+",server=on,wait=off")
	}
	cmd = exec.Command(qemuPath, args...)
	cmd.Env = append(os.Environ(), "OMARCHY_QEMU_BALLOON_DECOMMIT=1")
	cmd.Stdout = io.Discard
	cmd.Stderr = stderr
	started := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	result["qemu_pid"] = cmd.Process.Pid
	done = make(chan struct{})
	go func() { processErr = cmd.Wait(); close(done) }()
	ctx, cancelAll := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancelAll()
	sshArgs := []string{"-p", strconv.Itoa(sshPort), "-i", sshKey,
		"-o", "BatchMode=yes", "-o", "LogLevel=ERROR", "-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=NUL", "-o", "ConnectTimeout=2", "z4mbo@127.0.0.1", "echo ready"}
	sshReady := func() bool {
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(probeCtx, "ssh.exe", sshArgs...).Output()
		return err == nil && strings.Contains(string(output), "ready")
	}
	for !sshReady() {
		select {
		case <-done:
			t.Fatalf("disposable QEMU exited before SSH: %v", processErr)
		default:
		}
		if time.Since(started) > 90*time.Second {
			t.Fatal("disposable guest did not enable SSH")
		}
		time.Sleep(time.Second)
	}
	result["ssh_ready_seconds"] = time.Since(started).Seconds()
	for time.Since(started) < 12*time.Second {
		time.Sleep(100 * time.Millisecond)
	}
	connect := func(path string) *qmpClient {
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", path)
		if err != nil {
			t.Fatalf("dial %s: %v", filepath.Base(path), err)
		}
		client, err := newQMPClient(ctx, conn)
		if err != nil {
			t.Fatalf("QMP handshake %s: %v", filepath.Base(path), err)
		}
		return client
	}
	for _, path := range otherPaths {
		monitor := connect(path)
		monitors = append(monitors, monitor)
		var status vmRuntimeStatus
		if err := monitor.Call(ctx, "query-status", nil, &status); err != nil || !status.Running {
			t.Fatalf("private monitor %s not running: %v", filepath.Base(path), err)
		}
	}
	result["independent_qmp_monitors_open"] = len(monitors)
	balloonPath := "/machine/peripheral/experimental-balloon"
	var reclaimActive bool
	if err := monitors[0].Call(ctx, "qom-get", map[string]any{
		"path": balloonPath + "/virtio-backend", "property": balloonReclaimCapabilityProperty,
	}, &reclaimActive); err != nil || !reclaimActive {
		t.Fatalf("experimental QOM attestation: %v, active=%v", err, reclaimActive)
	}
	result["qom_reclaim_active"] = reclaimActive
	readGuest := func() (int64, int64, int64) {
		var balloon struct {
			Actual int64 `json:"actual"`
		}
		if err := monitors[0].Call(ctx, "query-balloon", nil, &balloon); err != nil {
			t.Fatal(err)
		}
		var stats struct {
			LastUpdate int64            `json:"last-update"`
			Stats      map[string]int64 `json:"stats"`
		}
		if err := monitors[0].Call(ctx, "qom-get", map[string]any{
			"path": balloonPath, "property": "guest-stats",
		}, &stats); err != nil {
			t.Fatal(err)
		}
		return balloon.Actual, stats.LastUpdate, stats.Stats["stat-available-memory"]
	}
	beforeActual, beforeStats, beforeGuestAvailable := readGuest()
	_, hostBefore := availMemMiB()
	result["balloon_before_bytes"] = beforeActual
	result["guest_stats_before_unix"] = beforeStats
	result["guest_available_before_bytes"] = beforeGuestAvailable
	result["windows_available_before_mib"] = hostBefore
	adapterCtx, cancelAdapter := context.WithCancel(ctx)
	stopAfter := time.AfterFunc(13*time.Second, cancelAdapter)
	adapterStarted := time.Now()
	adapterErr := runExperimentalBalloonOnWindows(adapterCtx, experimentalBalloonOptions{
		QEMUPath: qemuPath, QEMUPID: cmd.Process.Pid, Control: control,
		BalloonPath: balloonPath, BootMiB: 4096, FloorMiB: 2048,
	})
	stopAfter.Stop()
	cancelAdapter()
	adapterDuration := time.Since(adapterStarted)
	result["adapter_duration_seconds"] = adapterDuration.Seconds()
	result["adapter_result"] = fmt.Sprint(adapterErr)
	if !errors.Is(adapterErr, context.Canceled) || adapterDuration < 12*time.Second {
		t.Fatalf("actual Windows adapter: %v", adapterErr)
	}
	afterActual, afterStats, afterGuestAvailable := readGuest()
	_, hostAfter := availMemMiB()
	result["balloon_after_bytes"] = afterActual
	result["guest_stats_after_unix"] = afterStats
	result["guest_available_after_bytes"] = afterGuestAvailable
	result["windows_available_after_mib"] = hostAfter
	result["ssh_after_adapter"] = sshReady()
	if !result["ssh_after_adapter"].(bool) || afterActual != 4096<<20 ||
		afterStats <= 0 || afterStats < time.Now().Add(-balloonGuestStatsMaxAge).Unix() || afterGuestAvailable <= 0 {
		t.Fatal("guest liveness, balloon size, or guest statistics invalid after adapter run")
	}
	for index, monitor := range monitors {
		var status vmRuntimeStatus
		if err := monitor.Call(ctx, "query-status", nil, &status); err != nil || !status.Running {
			t.Fatalf("private monitor %d stopped after adapter: %v", index, err)
		}
	}
	result["other_monitors_after_adapter"] = len(monitors)
	if err := monitors[0].Call(ctx, "system_powerdown", nil, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("disposable ACPI shutdown: %v", processErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("disposable guest did not shut down")
	}
	result["clean_shutdown"] = true
	t.Logf("actual Windows adapter passed with four private QMP endpoints; evidence: %s", evidenceDir)
}
