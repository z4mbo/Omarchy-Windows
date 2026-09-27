package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

const guestUpdatePlanName = "guest-update-plan.json"
const maxGuestUpdatePlanBytes = 2 << 20
const maxGuestUpdatePackages = 4096

var (
	guestPackageNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9@._+-]{0,127}$`)
	guestPackageVersionPattern = regexp.MustCompile(`^(?:[0-9]+:)?[A-Za-z0-9][A-Za-z0-9.+_~]*-[A-Za-z0-9][A-Za-z0-9.+_~]*$`)
	guestKernelReleasePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
)

// This is authenticated metadata only. No current launcher path applies the
// package plan or changes the writable guest disk.
type guestUpdatePlan struct {
	Schema   int                  `json:"schema"`
	Baseline guestUpdateBaseline  `json:"baseline"`
	Target   guestUpdateTarget    `json:"target"`
	Packages []guestUpdatePackage `json:"packages"`
}

type guestUpdateBaseline struct {
	Release               string                     `json:"release"`
	ReleaseManifestSHA256 string                     `json:"releaseManifestSHA256"`
	ManagedPackages       []guestPackagePrecondition `json:"managedPackages"`
}

type guestPackagePrecondition struct {
	Name             string `json:"name"`
	InstalledVersion string `json:"installedVersion,omitempty"`
	Absent           *bool  `json:"absent,omitempty"`
}

type guestUpdateTarget struct {
	Release        string `json:"release"`
	Architecture   string `json:"architecture"`
	KernelRelease  string `json:"kernelRelease"`
	CompatRevision int    `json:"compatRevision"`
}

type guestUpdatePackage struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
}

// fetchSignedGuestUpdatePlan follows the same trust chain as launcher updates:
// Ed25519 update metadata -> release SHA256SUMS digest -> exact plan and package
// hashes. The caller supplies the normal embedded update public key; this
// function is deliberately not wired into the current update flow.
func fetchSignedGuestUpdatePlan(client *http.Client, manifestURL string, publicKey ed25519.PublicKey) (*guestUpdatePlan, error) {
	manifest, err := fetchUpdateManifest(client, manifestURL, publicKey)
	if err != nil {
		return nil, err
	}
	sums, err := releaseSums(client, manifest.Release, manifest.ManifestSHA256)
	if err != nil {
		return nil, fmt.Errorf("authenticating guest update release: %w", err)
	}
	expected := sums[guestUpdatePlanName]
	if !validSHA256(expected) {
		return nil, errors.New("authenticated release has no guest update plan")
	}
	data, err := fetchSmallFile(client, normalizedRelease(manifest.Release)+"/"+guestUpdatePlanName, maxGuestUpdatePlanBytes)
	if err != nil {
		return nil, fmt.Errorf("downloading guest update plan: %w", err)
	}
	actual := sha256.Sum256(data)
	if hex.EncodeToString(actual[:]) != expected {
		return nil, errors.New("guest update plan authentication failed")
	}
	plan, err := parseGuestUpdatePlan(data)
	if err != nil {
		return nil, err
	}
	if err := validateGuestUpdatePlan(plan, manifest, sums); err != nil {
		return nil, err
	}
	return plan, nil
}

func parseGuestUpdatePlan(data []byte) (*guestUpdatePlan, error) {
	if len(data) == 0 || len(data) > maxGuestUpdatePlanBytes {
		return nil, errors.New("guest update plan has an invalid size")
	}
	if err := rejectDuplicateGuestPlanFields(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var plan guestUpdatePlan
	if err := dec.Decode(&plan); err != nil {
		return nil, fmt.Errorf("parsing guest update plan: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("guest update plan contains trailing data")
	}
	return &plan, nil
}

func rejectDuplicateGuestPlanFields(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanGuestPlanJSONValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("guest update plan contains trailing data")
	}
	return nil
}

func scanGuestPlanJSONValue(dec *json.Decoder, depth int) error {
	if depth > 8 {
		return errors.New("guest update plan nesting is too deep")
	}
	token, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid guest update plan JSON: %w", err)
	}
	if token == nil {
		return errors.New("guest update plan contains null")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			nameToken, err := dec.Token()
			if err != nil {
				return fmt.Errorf("invalid guest update plan object: %w", err)
			}
			name, ok := nameToken.(string)
			// encoding/json matches struct fields without regard to case. Reject
			// aliases that would otherwise overwrite the same plan field. Its
			// Unicode SimpleFold also equates some non-ASCII characters with
			// ASCII letters, unlike strings.ToLower, so plan keys stay ASCII.
			if !ok {
				return errors.New("guest update plan contains an invalid field")
			}
			for _, char := range name {
				if char > 127 {
					return errors.New("guest update plan contains a non-ASCII field")
				}
			}
			folded := strings.ToLower(name)
			if seen[folded] {
				return errors.New("guest update plan contains a duplicate or invalid field")
			}
			seen[folded] = true
			if err := scanGuestPlanJSONValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanGuestPlanJSONValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("guest update plan contains an invalid delimiter")
	}
	closing, err := dec.Token()
	if err != nil {
		return fmt.Errorf("invalid guest update plan delimiter: %w", err)
	}
	expected := json.Delim('}')
	if delim == '[' {
		expected = json.Delim(']')
	}
	if closing != expected {
		return errors.New("guest update plan has mismatched delimiters")
	}
	return nil
}

func validateGuestUpdatePlan(plan *guestUpdatePlan, manifest *updateManifest, sums map[string]string) error {
	if plan == nil || manifest == nil || plan.Schema != 1 {
		return errors.New("unsupported guest update plan schema")
	}
	if _, ok := parseReleaseVersion(plan.Baseline.Release); !ok ||
		plan.Target.Release != manifest.Version || !updateIsNewer(plan.Target.Release, plan.Baseline.Release) {
		return errors.New("guest update plan release does not match the signed update")
	}
	if !validSHA256(plan.Baseline.ReleaseManifestSHA256) ||
		plan.Baseline.ReleaseManifestSHA256 != strings.ToLower(plan.Baseline.ReleaseManifestSHA256) {
		return errors.New("invalid baseline release manifest digest")
	}
	// The shipped VM and guest image are x86_64. A different guest architecture
	// needs its own package and boot compatibility contract in a later schema.
	if plan.Target.Architecture != "x86_64" {
		return errors.New("unsupported guest update architecture")
	}
	if !guestKernelReleasePattern.MatchString(plan.Target.KernelRelease) ||
		plan.Target.CompatRevision < 1 || plan.Target.CompatRevision > 999999 {
		return errors.New("invalid guest update kernel or compatibility revision")
	}
	if len(plan.Packages) == 0 || len(plan.Packages) > maxGuestUpdatePackages ||
		len(plan.Baseline.ManagedPackages) != len(plan.Packages) {
		return errors.New("guest update package or precondition count is invalid")
	}
	packages := make(map[string]bool, len(plan.Packages))
	filenames := make(map[string]bool, len(plan.Packages))
	for _, pkg := range plan.Packages {
		if !guestPackageNamePattern.MatchString(pkg.Name) || !guestPackageVersionPattern.MatchString(pkg.Version) ||
			(pkg.Architecture != plan.Target.Architecture && pkg.Architecture != "any") ||
			!validSHA256(pkg.SHA256) || pkg.SHA256 != strings.ToLower(pkg.SHA256) {
			return errors.New("guest update package identity is invalid")
		}
		archiveVersion := pkg.Version
		if index := strings.IndexByte(archiveVersion, ':'); index >= 0 {
			archiveVersion = archiveVersion[index+1:]
		}
		filename := pkg.Name + "-" + archiveVersion + "-" + pkg.Architecture + ".pkg.tar.zst"
		if pkg.Filename != filename || packages[pkg.Name] || filenames[pkg.Filename] || sums[pkg.Filename] != pkg.SHA256 {
			return fmt.Errorf("guest update package %q is duplicate or differs from authenticated release", pkg.Name)
		}
		packages[pkg.Name], filenames[pkg.Filename] = true, true
	}
	preconditions := make(map[string]bool, len(plan.Baseline.ManagedPackages))
	for _, condition := range plan.Baseline.ManagedPackages {
		if !packages[condition.Name] || preconditions[condition.Name] ||
			(condition.Absent == nil && !guestPackageVersionPattern.MatchString(condition.InstalledVersion)) ||
			(condition.Absent != nil && (!*condition.Absent || condition.InstalledVersion != "")) {
			return fmt.Errorf("invalid or duplicate guest package precondition %q", condition.Name)
		}
		preconditions[condition.Name] = true
	}
	return nil
}
