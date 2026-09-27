//go:build windows && candidate_acceptance

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This deliberately exercises the launcher's first-install setup functions,
// without invoking the window, machine registration, shortcuts, or QEMU.
// The inputs are a verified candidate payload and a never-used disposable dir.
func TestCandidateFreshPortableSetup(t *testing.T) {
	payload := os.Getenv("OMARCHY_CANDIDATE_PAYLOAD")
	dataDir := os.Getenv("OMARCHY_CANDIDATE_DATA")
	manifestSHA := normalizedSHA256(os.Getenv("OMARCHY_CANDIDATE_MANIFEST_SHA256"))
	if payload == "" || dataDir == "" || !validSHA256(manifestSHA) {
		t.Fatal("set OMARCHY_CANDIDATE_PAYLOAD, OMARCHY_CANDIDATE_DATA, and OMARCHY_CANDIDATE_MANIFEST_SHA256")
	}
	payload, err := filepath.Abs(payload)
	if err != nil {
		t.Fatal(err)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(parent), strings.ToLower(tempDir)+string(os.PathSeparator)) ||
		!strings.HasPrefix(filepath.Base(dataDir), "setup-acceptance-data") ||
		strings.EqualFold(dataDir, payload) {
		t.Fatalf("disposable data directory must be a new setup-acceptance-data* directory below %s", tempDir)
	}
	if _, err := os.Lstat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("disposable data directory must not exist before this test: %v", err)
	}
	sums, err := readPortableManifest(filepath.Join(payload, "SHA256SUMS"), manifestSHA)
	if err != nil {
		t.Fatalf("candidate manifest: %v", err)
	}
	// This URL is intentionally unreachable. Any accidental online fallback fails.
	const release = "http://127.0.0.1:9/candidate-36253111377"
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config{
		dir: dataDir, hostDir: dataDir, payloadDir: payload,
		guestDir: filepath.Join(dataDir, "guest"),
		vmDir:    filepath.Join(dataDir, "vm"),
		portable: true, instant: true, diskFormat: "qcow2",
	}
	cfg.disk = filepath.Join(cfg.vmDir, "disk.qcow2")
	if err := os.Mkdir(cfg.vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configureSetupCancellation(false)
	uiOnce.Do(func() { uiSingleton = &progressUI{} }) // inert: no Win32 splash
	chooseProvisionMode(cfg, true)
	if mode, ok := readProvisionMode(dataDir); !ok || mode != provisionModeInstant {
		t.Fatalf("provision mode = %q, %t; want instant", mode, ok)
	}
	runtimeRoot, err := ensureRuntime(cfg, release, manifestSHA)
	if err != nil {
		t.Fatalf("runtime installation: %v", err)
	}
	if err := ensureGuest(cfg, release, manifestSHA); err != nil {
		t.Fatalf("guest installation: %v", err)
	}
	var spec buildSpec
	buildSpecBytes, err := os.ReadFile(filepath.Join(cfg.guestDir, "build-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buildSpecBytes, &spec); err != nil {
		t.Fatal(err)
	}
	if err := prepareDisk(cfg, spec.Runtime.Storage.ExpandedSizeMiB); err != nil {
		t.Fatalf("writable disk preparation: %v", err)
	}
	if !completeInstallExists(dataDir, "disk.qcow2") {
		t.Fatal("complete install files are missing")
	}
	if !runtimeReceiptMatches(runtimeRoot, release, manifestSHA, sums[runtimeZip]) {
		t.Fatal("runtime receipt does not match verified archive")
	}
	guestReady, err := installReceiptMatches(cfg.guestDir, release, manifestSHA, installedGuestArtifacts)
	if err != nil || !guestReady {
		t.Fatalf("guest receipt mismatch: ready=%t err=%v", guestReady, err)
	}
	rootfs := filepath.Join(cfg.guestDir, "rootfs.ext4")
	if ok, err := verifyFileSHA256(rootfs, sums["rootfs.ext4"], nil); err != nil || !ok {
		t.Fatalf("decompressed rootfs checksum mismatch: ok=%t err=%v", ok, err)
	}
	qemu := filepath.Join(runtimeRoot, "bin", "qemu-system-x86_64w.exe")
	const expectedQemuSHA = "7b3aad5f42c1e7287ffb80dcbe73882deb8fe797cc55feff0ab8f5e1f53c0643"
	if ok, err := verifyFileSHA256(qemu, expectedQemuSHA, nil); err != nil || !ok {
		t.Fatalf("pinned QEMU executable checksum mismatch: ok=%t err=%v", ok, err)
	}
	virtualMiB, err := requestedDiskMiB(spec.Runtime.Storage.ExpandedSizeMiB, cfg.diskGiB, true)
	if err != nil {
		t.Fatal(err)
	}
	backing := filepath.ToSlash(filepath.Join("..", "guest", "rootfs.ext4"))
	if ok, err := qcow2OverlayMatches(cfg.disk, backing, virtualMiB*1024*1024); err != nil || !ok {
		t.Fatalf("QCOW2 overlay mismatch: ok=%t err=%v", ok, err)
	}
	if ok, err := portableBackingStateMatches(cfg.disk, sums["rootfs.ext4"]); err != nil || !ok {
		t.Fatalf("QCOW2 backing identity mismatch: ok=%t err=%v", ok, err)
	}
	paths := []string{
		filepath.Join(runtimeRoot, runtimeReceiptFilename),
		filepath.Join(cfg.guestDir, installReceiptFilename),
		cfg.disk,
	}
	before := make([]int64, len(paths))
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[i] = info.ModTime().UnixNano()
	}
	if _, err := ensureRuntime(cfg, release, manifestSHA); err != nil {
		t.Fatalf("idempotent runtime setup: %v", err)
	}
	if err := ensureGuest(cfg, release, manifestSHA); err != nil {
		t.Fatalf("idempotent guest setup: %v", err)
	}
	if err := prepareDisk(cfg, spec.Runtime.Storage.ExpandedSizeMiB); err != nil {
		t.Fatalf("idempotent disk setup: %v", err)
	}
	for i, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().UnixNano() != before[i] {
			t.Fatalf("second setup pass changed %s", path)
		}
	}
	for _, path := range []string{filepath.Join(dataDir, "runtime.part"), cfg.disk + ".part", rootfs + ".part"} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("staging file remains: %s (%v)", path, err)
		}
	}
	t.Logf("PASS: runtime and guest receipts, full rootfs and QEMU hashes, %d MiB QCOW2 overlay, backing identity, and unchanged second setup pass in %s", virtualMiB, dataDir)
}

// The normal first install downloads a verified release and creates a sparse
// raw disk. A loopback server supplies the already authenticated candidate so
// this test covers that production path without reaching the internet.
func TestCandidateFreshStandardSetup(t *testing.T) {
	payload := os.Getenv("OMARCHY_CANDIDATE_PAYLOAD")
	dataDir := os.Getenv("OMARCHY_CANDIDATE_DATA_STANDARD")
	manifestSHA := normalizedSHA256(os.Getenv("OMARCHY_CANDIDATE_MANIFEST_SHA256"))
	if payload == "" || dataDir == "" || !validSHA256(manifestSHA) {
		t.Fatal("set OMARCHY_CANDIDATE_PAYLOAD, OMARCHY_CANDIDATE_DATA_STANDARD, and OMARCHY_CANDIDATE_MANIFEST_SHA256")
	}
	var err error
	payload, err = filepath.Abs(payload)
	if err != nil {
		t.Fatal(err)
	}
	dataDir, err = filepath.Abs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	tempDir, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.ToLower(parent), strings.ToLower(tempDir)+string(os.PathSeparator)) ||
		!strings.HasPrefix(filepath.Base(dataDir), "setup-acceptance-standard") ||
		strings.EqualFold(dataDir, payload) {
		t.Fatalf("disposable data directory must be a new setup-acceptance-standard* directory below %s", tempDir)
	}
	if _, err := os.Lstat(dataDir); !os.IsNotExist(err) {
		t.Fatalf("disposable data directory must not exist before this test: %v", err)
	}
	sums, err := readPortableManifest(filepath.Join(payload, "SHA256SUMS"), manifestSHA)
	if err != nil {
		t.Fatalf("candidate manifest: %v", err)
	}
	server := httptest.NewServer(http.FileServer(http.Dir(payload)))
	defer server.Close()
	if err := os.Mkdir(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &config{
		dir: dataDir, hostDir: dataDir, instant: true,
		guestDir:   filepath.Join(dataDir, "guest"),
		vmDir:      filepath.Join(dataDir, "vm"),
		diskFormat: "raw",
	}
	cfg.disk = filepath.Join(cfg.vmDir, "disk.raw")
	if err := os.Mkdir(cfg.vmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configureSetupCancellation(false)
	uiOnce.Do(func() { uiSingleton = &progressUI{} })
	chooseProvisionMode(cfg, true)
	if mode, ok := readProvisionMode(dataDir); !ok || mode != provisionModeInstant {
		t.Fatalf("provision mode = %q, %t; want instant", mode, ok)
	}
	runtimeRoot, err := ensureRuntime(cfg, server.URL, manifestSHA)
	if err != nil {
		t.Fatalf("runtime download/install: %v", err)
	}
	if err := ensureGuest(cfg, server.URL, manifestSHA); err != nil {
		t.Fatalf("guest download/install: %v", err)
	}
	var spec buildSpec
	buildSpecBytes, err := os.ReadFile(filepath.Join(cfg.guestDir, "build-spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buildSpecBytes, &spec); err != nil {
		t.Fatal(err)
	}
	if err := prepareDisk(cfg, spec.Runtime.Storage.ExpandedSizeMiB); err != nil {
		t.Fatalf("sparse raw disk preparation: %v", err)
	}
	if !completeInstallExists(dataDir, "disk.raw") || !runtimeReceiptMatches(runtimeRoot, server.URL, manifestSHA, sums[runtimeZip]) {
		t.Fatal("complete standard install files or runtime receipt are missing")
	}
	guestReady, err := installReceiptMatches(cfg.guestDir, server.URL, manifestSHA, installedGuestArtifacts)
	if err != nil || !guestReady {
		t.Fatalf("guest receipt mismatch: ready=%t err=%v", guestReady, err)
	}
	virtualMiB, err := requestedDiskMiB(spec.Runtime.Storage.ExpandedSizeMiB, cfg.diskGiB, false)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.disk)
	if err != nil || info.Size() != virtualMiB*1024*1024 {
		t.Fatalf("raw disk size: info=%v err=%v", info, err)
	}
	rootfsInfo, err := os.Stat(filepath.Join(cfg.guestDir, "rootfs.ext4"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cfg.disk)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	_, copyErr := io.CopyN(h, f, rootfsInfo.Size())
	closeErr := f.Close()
	if copyErr != nil || closeErr != nil || hex.EncodeToString(h.Sum(nil)) != sums["rootfs.ext4"] {
		t.Fatalf("raw disk factory prefix checksum mismatch: copy=%v close=%v", copyErr, closeErr)
	}
	before := info.ModTime().UnixNano()
	if _, err := ensureRuntime(cfg, server.URL, manifestSHA); err != nil {
		t.Fatalf("idempotent runtime setup: %v", err)
	}
	if err := ensureGuest(cfg, server.URL, manifestSHA); err != nil {
		t.Fatalf("idempotent guest setup: %v", err)
	}
	if err := prepareDisk(cfg, spec.Runtime.Storage.ExpandedSizeMiB); err != nil {
		t.Fatalf("idempotent raw disk setup: %v", err)
	}
	info, err = os.Stat(cfg.disk)
	if err != nil || info.ModTime().UnixNano() != before {
		t.Fatalf("second setup pass changed raw disk: %v", err)
	}
	t.Logf("PASS: loopback release download, verified receipts and decompression, %d MiB sparse raw disk with authenticated factory prefix, and unchanged second pass in %s", virtualMiB, dataDir)
}
