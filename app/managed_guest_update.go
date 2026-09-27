package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// This coordinator is deliberately dormant. A future update executor must
// invoke RecoverPending before any VM launch, and must not write packages
// until Prepare has returned a durable journal. No generic guest-ready event
// can discard this journal or declare a package transaction healthy.
const managedGuestUpdateJournalName = ".managed-guest-update.json"

type managedGuestUpdateJournal struct {
	Version             int    `json:"version"`
	BaselineRelease     string `json:"baselineRelease"`
	BaselineManifestSHA string `json:"baselineManifestSHA256"`
	TargetRelease       string `json:"targetRelease"`
	PlanSHA256          string `json:"planSHA256"`
	CheckpointID        string `json:"checkpointID"`
	CheckpointSHA256    string `json:"checkpointSHA256"`
	CheckpointBytes     int64  `json:"checkpointBytes"`
	CheckpointArch      string `json:"checkpointArchitecture"`
	DiskFormat          string `json:"diskFormat"`
	DiskName            string `json:"diskName"`
}

type managedGuestUpdateCoordinator struct {
	installation string
}

func (c managedGuestUpdateCoordinator) journalPath() string {
	return filepath.Join(c.installation, managedGuestUpdateJournalName)
}

// Prepare authenticates the update metadata before creating a full stopped-VM
// checkpoint. It does not download packages, run pacman, or alter the guest.
func (c managedGuestUpdateCoordinator) Prepare(client *http.Client, manifestURL string, publicKey ed25519.PublicKey, report backupProgress) (managedGuestUpdateJournal, error) {
	plan, err := fetchSignedGuestUpdatePlan(client, manifestURL, publicKey)
	if err != nil {
		return managedGuestUpdateJournal{}, err
	}
	release, _, ok := installReceiptIdentity(filepath.Join(c.installation, "guest"))
	if !ok || !managedGuestBaselineReleaseMatches(release, plan.Baseline.Release) {
		return managedGuestUpdateJournal{}, errors.New("installed guest does not match the authenticated update baseline")
	}
	baselineSums, err := releaseSums(client, release, plan.Baseline.ReleaseManifestSHA256)
	if err != nil {
		return managedGuestUpdateJournal{}, fmt.Errorf("authenticating installed guest baseline: %w", err)
	}
	return c.prepareVerifiedPlan(plan, baselineSums, report)
}

// prepareVerifiedPlan is only reached with a plan already authenticated by
// fetchSignedGuestUpdatePlan. It is separate for small offline state tests.
func (c managedGuestUpdateCoordinator) prepareVerifiedPlan(plan *guestUpdatePlan, baselineSums map[string]string, report backupProgress) (managedGuestUpdateJournal, error) {
	var journal managedGuestUpdateJournal
	if plan == nil || plan.Schema != 1 || !validSHA256(plan.Baseline.ReleaseManifestSHA256) ||
		plan.Target.Architecture != "x86_64" || !guestKernelReleasePattern.MatchString(plan.Target.KernelRelease) ||
		plan.Target.CompatRevision < 1 || len(plan.Packages) == 0 ||
		!updateIsNewer(plan.Target.Release, plan.Baseline.Release) {
		return journal, errors.New("guest update plan is invalid")
	}
	if err := validateMovePath(c.installation); err != nil {
		return journal, err
	}
	guard, err := lockMoveStore(moveStore{dir: c.installation})
	if err != nil {
		return journal, err
	}
	defer guard.Close()
	if err := recoverCheckpointRollback(c.installation); err != nil {
		return journal, err
	}
	if _, err := readManagedGuestUpdateJournal(c.journalPath()); err == nil {
		return journal, errors.New("recover the pending managed guest update before preparing another")
	} else if !os.IsNotExist(err) {
		return journal, err
	}
	for _, name := range []string{payloadUpdateStateFilename, updateStateFilename} {
		if _, err := os.Lstat(filepath.Join(c.installation, name)); !os.IsNotExist(err) {
			return journal, errors.New("finish the pending update before preparing a managed guest update")
		}
	}
	guestDir := filepath.Join(c.installation, "guest")
	release, manifestSHA, ok := installReceiptIdentity(guestDir)
	if !ok || manifestSHA != plan.Baseline.ReleaseManifestSHA256 || !managedGuestBaselineReleaseMatches(release, plan.Baseline.Release) {
		return journal, errors.New("installed guest does not match the authenticated update baseline")
	}
	ready, err := installReceiptMatches(guestDir, release, manifestSHA, installedGuestArtifacts)
	if err != nil || !ready {
		return journal, errors.New("installed guest payload is not verified for this update baseline")
	}
	if err := verifyManagedGuestBaselineFiles(guestDir, baselineSums); err != nil {
		return journal, err
	}
	disk, err := inspectInstallationDisk(c.installation)
	if err != nil {
		return journal, err
	}
	diskLock, err := openBackupDisk(disk.Path)
	if err != nil {
		return journal, fmt.Errorf("close Omarchy before preparing a managed guest update: %w", err)
	}
	diskLock.Close()
	planBytes, err := json.Marshal(plan)
	if err != nil {
		return journal, err
	}
	planHash := sha256.Sum256(planBytes)
	checkpoint, err := (checkpointStore{installation: c.installation}).Create("Before guest update to "+plan.Target.Release, report)
	if err != nil {
		return journal, err
	}
	if checkpoint.Architecture != plan.Target.Architecture {
		return journal, errors.New("guest update checkpoint architecture differs from the authenticated plan")
	}
	journal = managedGuestUpdateJournal{
		Version: 1, BaselineRelease: plan.Baseline.Release, BaselineManifestSHA: manifestSHA,
		TargetRelease: plan.Target.Release, PlanSHA256: hex.EncodeToString(planHash[:]),
		CheckpointID: checkpoint.ID, CheckpointSHA256: checkpoint.ArchiveSHA256,
		CheckpointBytes: checkpoint.ArchiveBytes, CheckpointArch: checkpoint.Architecture,
		DiskFormat: disk.Format, DiskName: "vm/disk." + disk.Format,
	}
	if err := saveManagedGuestUpdateJournal(c.journalPath(), journal); err != nil {
		// The completed checkpoint is safe to retain. No guest write is allowed
		// because Prepare did not return a durable journal.
		return managedGuestUpdateJournal{}, err
	}
	return journal, nil
}

func managedGuestUpdateDisk(dir, format, name string) (installationDisk, error) {
	if (format != "raw" && format != "qcow2") || name != "vm/disk."+format {
		return installationDisk{}, errors.New("invalid managed guest update disk identity")
	}
	other := "raw"
	if format == "raw" {
		other = "qcow2"
	}
	if _, err := os.Lstat(filepath.Join(dir, "vm", "disk."+other)); !os.IsNotExist(err) {
		return installationDisk{}, errors.New("another guest disk exists; recovery remains blocked")
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := validateMovePath(path); err != nil {
		return installationDisk{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return installationDisk{}, err
	}
	if err := rejectMoveLink(path, info); err != nil {
		return installationDisk{}, err
	}
	if !info.Mode().IsRegular() {
		return installationDisk{}, errors.New("active guest disk is not a regular file")
	}
	// The current disk bytes, including a QCOW2 header, may be torn. Only the
	// pre-update journal chooses the restoration format; the exclusive open in
	// rollbackWithToolPolicy proves this exact path is not held by the VM.
	return installationDisk{Path: path, Format: format}, nil
}

func verifyManagedGuestBaselineFiles(guestDir string, sums map[string]string) error {
	for _, name := range installedGuestArtifacts {
		want := sums[name]
		if !validSHA256(want) || want != strings.ToLower(want) {
			return fmt.Errorf("authenticated baseline has no valid digest for %s", name)
		}
		path := filepath.Join(guestDir, name)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if err := rejectMoveLink(path, info); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("invalid installed guest file %s", name)
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if hex.EncodeToString(h.Sum(nil)) != want {
			return fmt.Errorf("installed guest file %s differs from authenticated baseline", name)
		}
	}
	return nil
}

func managedGuestBaselineReleaseMatches(receiptURL, baselineTag string) bool {
	if _, ok := parseReleaseVersion(baselineTag); !ok {
		return false
	}
	for _, prefix := range []string{forkReleaseBase, legacyReleaseBase, transferredReleaseBase, officialReleaseBase} {
		if receiptURL == prefix+baselineTag {
			return true
		}
	}
	return false
}

func validManagedGuestUpdateJournal(j managedGuestUpdateJournal) bool {
	_, baselineOK := parseReleaseVersion(j.BaselineRelease)
	if j.Version != 1 || !validCheckpointID(j.CheckpointID) ||
		!validSHA256(j.BaselineManifestSHA) || !validSHA256(j.PlanSHA256) ||
		!validSHA256(j.CheckpointSHA256) || j.CheckpointBytes <= 0 || j.CheckpointBytes > backupMaxBytes ||
		!baselineOK || j.CheckpointArch != "x86_64" ||
		(j.DiskFormat != "raw" && j.DiskFormat != "qcow2") || j.DiskName != "vm/disk."+j.DiskFormat ||
		!updateIsNewer(j.TargetRelease, j.BaselineRelease) {
		return false
	}
	return j.BaselineManifestSHA == strings.ToLower(j.BaselineManifestSHA) &&
		j.PlanSHA256 == strings.ToLower(j.PlanSHA256) && j.CheckpointSHA256 == strings.ToLower(j.CheckpointSHA256)
}

func readManagedGuestUpdateJournal(path string) (managedGuestUpdateJournal, error) {
	var state managedGuestUpdateJournal
	info, err := os.Lstat(path)
	if err != nil {
		return state, err
	}
	if err := rejectMoveLink(path, info); err != nil {
		return state, err
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 4096 {
		return state, errors.New("invalid managed guest update journal file")
	}
	f, err := os.Open(path)
	if err != nil {
		return state, err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 4097))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&state); err != nil {
		return state, err
	}
	if dec.Decode(new(any)) != io.EOF || !validManagedGuestUpdateJournal(state) {
		return state, errors.New("invalid managed guest update journal")
	}
	return state, nil
}

func saveManagedGuestUpdateJournal(path string, state managedGuestUpdateJournal) error {
	if !validManagedGuestUpdateJournal(state) {
		return errors.New("invalid managed guest update journal")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".managed-guest-update-pending-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	err = json.NewEncoder(f).Encode(state)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("managed guest update journal already exists")
	}
	return publishMoveFile(f.Name(), path)
}

// RecoverPending is the explicit pre-launch gate for a future updater. Any
// pending journal is rolled back, including a crash between checkpoint and the
// first package write. It never silently accepts a missing or changed archive.
func (c managedGuestUpdateCoordinator) RecoverPending(report backupProgress) (bool, error) {
	if err := validateMovePath(c.installation); err != nil {
		return false, err
	}
	guard, err := lockMoveStore(moveStore{dir: c.installation})
	if err != nil {
		return false, err
	}
	defer guard.Close()
	if err := recoverCheckpointRollback(c.installation); err != nil {
		return false, err
	}
	journal, err := readManagedGuestUpdateJournal(c.journalPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	store := checkpointStore{installation: c.installation}
	root, err := store.open(false)
	if err != nil {
		return false, err
	}
	checkpoint, err := readCheckpoint(root, journal.CheckpointID)
	root.Close()
	if err != nil || checkpoint.ArchiveSHA256 != journal.CheckpointSHA256 || checkpoint.ArchiveBytes != journal.CheckpointBytes || checkpoint.Architecture != journal.CheckpointArch {
		return false, errors.New("managed guest update checkpoint identity changed; launch remains blocked")
	}
	if _, err := store.rollbackManagedGuestUpdate(journal.CheckpointID, journal.CheckpointArch, journal.DiskFormat, journal.DiskName, report); err != nil {
		return false, err
	}
	if err := os.Remove(c.journalPath()); err != nil {
		return false, fmt.Errorf("managed guest update restored but journal removal failed: %w", err)
	}
	return true, nil
}
