//go:build windows

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Keep this separate from the Omarchy image: Sunshine runs on Windows and
// streams Windows applications into Moonlight inside the guest. The digest is
// the published official MSI's SHA256, independently checked on a Windows PC.
const (
	sunshineVersion   = "2026.914.233613"
	sunshineMSIURL    = "https://github.com/LizardByte/Sunshine/releases/download/v" + sunshineVersion + "/Sunshine-Windows-AMD64-installer.msi"
	sunshineMSISHA256 = "1d7fed8beecd5889dc7ff14cf9f42d6d38f37c3066c13c6c2a5f4e91847e0ccf"
	sunshineService   = "SunshineService"
	sunshineWebURL    = "https://localhost:47990"
)

var sunshineStatePattern = regexp.MustCompile(`(?m)^\s*STATE\s+:\s+(\d+)\b`)

var procSHGetFolderPathW = syscall.NewLazyDLL("shell32.dll").NewProc("SHGetFolderPathW")

var errSunshineRebootRequired = errors.New("Sunshine installation needs a Windows restart")

func sunshineInstallerPath() (string, error) {
	local := os.Getenv("LOCALAPPDATA")
	if local == "" {
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(local, "Omarchy", "downloads", "Sunshine-"+sunshineVersion+"-AMD64.msi"), nil
}

func sunshineInstallDirectory() (string, error) {
	programFiles, err := windowsProgramFilesPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(programFiles, "Sunshine"), nil
}

// Resolve the system's Program Files folder through Windows, not the caller's
// environment, which can be altered before an elevated helper is launched.
func windowsProgramFilesPath() (string, error) {
	const csidlProgramFiles = 0x26
	var path [260]uint16
	hr, _, _ := procSHGetFolderPathW.Call(0, csidlProgramFiles, 0, 0, uintptr(unsafe.Pointer(&path[0])))
	if int32(hr) != 0 {
		return "", fmt.Errorf("Windows could not locate Program Files (HRESULT 0x%x)", uint32(hr))
	}
	value := syscall.UTF16ToString(path[:])
	if value == "" || !filepath.IsAbs(value) {
		return "", errors.New("Windows returned an invalid Program Files folder")
	}
	return value, nil
}

func sunshineInstalled() bool {
	dir, err := sunshineInstallDirectory()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "sunshine.exe"))
	return err == nil && info.Mode().IsRegular()
}

// sunshineReady is intentionally cheap enough for a menu label/status check.
// It confirms the host is configured for local access and the UI is listening.
func sunshineReady() bool {
	dir, err := sunshineInstallDirectory()
	if err != nil || !sunshineInstalled() {
		return false
	}
	config, err := os.ReadFile(filepath.Join(dir, "config", "sunshine.conf"))
	if err != nil || !sunshineBoundToLoopback(string(config)) {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1:47990", 150*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// runSunshineSetup is user initiated from Omarchy's Windows UI. It does not
// handle credentials or pair a client: Sunshine's local web UI owns both.
func runSunshineSetup() error {
	if reason := hostArchUnsupportedReason(); reason != "" {
		return errors.New(reason)
	}
	if sunshineReady() {
		return shellOpen(sunshineWebURL)
	}
	if msgBox("Set up Windows Desktop streaming for Omarchy?\n\nOmarchy will install Sunshine on Windows if needed. Windows may ask for administrator permission. Sunshine will accept connections only from this PC; no LAN firewall exception will be added. The Windows screen may briefly change while the service starts.", mbYesNo|mbIconQuestion) != idYes {
		return nil
	}
	ui := getUI()
	defer uiDone()
	if !sunshineInstalled() {
		installer, err := sunshineInstallerPath()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(installer), 0o755); err != nil {
			return err
		}
		ui.setStatus("Downloading Windows Desktop streaming...")
		if err := downloadVerified(newDownloadClient(), sunshineMSIURL, installer, sunshineMSISHA256, func(phase string, done, total int64) {
			if phase == downloadPhaseVerify {
				ui.setStatus("Checking Windows Desktop installer...")
			}
			ui.setProgress(done, total)
		}); err != nil {
			return fmt.Errorf("downloading the verified Sunshine installer: %w", err)
		}
	}
	// The UAC prompt and Windows Installer progress window take over here.
	// Close Omarchy's cancellable download panel first; installation cannot
	// safely be cancelled through that panel once Windows Installer starts.
	uiDone()
	code, err := runElevated("-sunshine-install")
	if err != nil {
		return err
	}
	if code == errorCancelled {
		return errors.New("Windows administrator permission was cancelled")
	}
	if code == dismRebootRequired {
		return errors.New("Sunshine needs a Windows restart before it can run")
	}
	if code != 0 {
		return fmt.Errorf("Sunshine setup did not finish (code %d)", code)
	}
	if !sunshineReady() {
		return errors.New("Sunshine was installed, but its local setup page is not responding yet")
	}
	return shellOpen(sunshineWebURL)
}

// runSunshineInstallElevated is called only by -sunshine-install in main. It
// rechecks the pinned digest under elevation, before invoking Windows Installer.
func runSunshineInstallElevated() int {
	if err := installSunshineLocal(); err != nil {
		logf("Sunshine setup: %v", err)
		if errors.Is(err, errSunshineRebootRequired) {
			return dismRebootRequired
		}
		return 1
	}
	return 0
}

func sunshineMSIArgs(installer string) []string {
	return []string{"/i", installer, "/passive", "/norestart", "ADDLOCAL=ALL", "REMOVE=CM_C_firewall"}
}

func sunshinePrivateStageDirectory() (string, error) {
	programFiles, err := windowsProgramFilesPath()
	if err != nil {
		return "", err
	}
	stageDir := filepath.Join(programFiles, "Omarchy")
	if err := os.Mkdir(stageDir, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(stageDir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("Omarchy's private installer directory is not a regular directory")
	}
	return stageDir, nil
}

// stageVerifiedSunshineMSI accepts a potentially user-writable cached MSI,
// copies it into the administrator-owned Program Files directory, then hashes
// those exact staged bytes before Windows Installer can read them. The caller
// owns removing the returned path. A random, exclusive name prevents reuse of
// an older staged MSI and avoids replacing an existing file or hard link.
func stageVerifiedSunshineMSI(source, privateDir, wantSHA string) (string, error) {
	const maxMSIBytes = 128 << 20
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxMSIBytes {
		return "", errors.New("cached Sunshine installer is not a regular MSI of the expected size")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	staged := filepath.Join(privateDir, "Sunshine-"+sunshineVersion+"-"+hex.EncodeToString(nonce[:])+".msi")
	out, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(staged)
		}
	}()
	written, copyErr := io.CopyN(out, in, maxMSIBytes+1)
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		out.Close()
		return "", copyErr
	}
	if written != info.Size() || written > maxMSIBytes {
		out.Close()
		return "", errors.New("cached Sunshine installer changed while staging")
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	ok, err := verifyFileSHA256(staged, wantSHA, nil)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", errors.New("the staged Sunshine installer failed the SHA256 check")
	}
	keep = true
	return staged, nil
}

func installSunshineLocal() error {
	dir, err := sunshineInstallDirectory()
	if err != nil {
		return err
	}
	confPath := filepath.Join(dir, "config", "sunshine.conf")
	if !sunshineInstalled() {
		installer, err := sunshineInstallerPath()
		if err != nil {
			return err
		}
		privateDir, err := sunshinePrivateStageDirectory()
		if err != nil {
			return err
		}
		staged, err := stageVerifiedSunshineMSI(installer, privateDir, sunshineMSISHA256)
		if err != nil {
			return err
		}
		defer os.Remove(staged)
		// Preseed the loopback setting before the MSI starts the service. The
		// firewall component is excluded by name from this pinned MSI.
		if err := setSunshineLoopback(confPath); err != nil {
			return fmt.Errorf("preparing local-only Sunshine configuration: %w", err)
		}
		cmd := exec.Command(system32("msiexec.exe"), sunshineMSIArgs(staged)...)
		if err := cmd.Run(); err != nil {
			if exit, ok := err.(*exec.ExitError); ok && (exit.ExitCode() == dismRebootRequired || exit.ExitCode() == 1641) {
				return errSunshineRebootRequired
			}
			return fmt.Errorf("Windows Installer: %w", err)
		}
		if !sunshineInstalled() {
			return errors.New("Windows Installer returned success without installing Sunshine")
		}
	}
	// An existing installation may have a broader bind address. Stop it
	// before changing that setting; then restart on the loopback address.
	content, err := os.ReadFile(confPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if !sunshineBoundToLoopback(string(content)) {
		if err := stopSunshineService(); err != nil {
			return err
		}
		if err := setSunshineLoopback(confPath); err != nil {
			return err
		}
	}
	if err := startSunshineService(); err != nil {
		return err
	}
	return nil
}

func sunshineBoundToLoopback(content string) bool {
	content = strings.TrimPrefix(content, "\ufeff")
	count := 0
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "bind_address") {
			count++
			if strings.TrimSpace(value) != "127.0.0.1" {
				return false
			}
		}
	}
	return count == 1
}

func localSunshineConfig(content string) string {
	content = strings.TrimPrefix(content, "\ufeff")
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "bind_address") {
			continue
		}
		lines = append(lines, line)
	}
	result := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	if result != "" {
		result += "\n"
	}
	return result + "bind_address = 127.0.0.1\n"
}

func setSunshineLoopback(path string) error {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if sunshineBoundToLoopback(string(old)) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	stage := path + ".omarchy-part"
	if err := os.WriteFile(stage, []byte(localSunshineConfig(string(old))), 0o600); err != nil {
		return err
	}
	defer os.Remove(stage)
	return replaceLauncher(stage, path)
}

func sunshineServiceState() (int, error) {
	cmd := exec.Command(system32("sc.exe"), "query", sunshineService)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	match := sunshineStatePattern.FindSubmatch(out)
	if len(match) != 2 {
		return 0, errors.New("could not read Sunshine service state")
	}
	return strconv.Atoi(string(match[1]))
}

func sunshineServiceCommand(action string) error {
	cmd := exec.Command(system32("sc.exe"), action, sunshineService)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("Sunshine service %s: %w (%s)", action, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func waitSunshineService(want int) error {
	for attempt := 0; attempt < 20; attempt++ {
		state, err := sunshineServiceState()
		if err != nil {
			return err
		}
		if state == want {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("Sunshine service did not reach state %d", want)
}

func stopSunshineService() error {
	state, err := sunshineServiceState()
	if err != nil {
		return err
	}
	if state == 1 {
		return nil
	}
	if err := sunshineServiceCommand("stop"); err != nil {
		return err
	}
	return waitSunshineService(1)
}

func startSunshineService() error {
	state, err := sunshineServiceState()
	if err != nil {
		return err
	}
	if state == 4 {
		return nil
	}
	if err := sunshineServiceCommand("start"); err != nil {
		return err
	}
	return waitSunshineService(4)
}
