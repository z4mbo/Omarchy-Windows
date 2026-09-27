package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func guestPlanTestDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validGuestUpdatePlanForTest() (*guestUpdatePlan, map[string]string, *updateManifest) {
	pkg := guestUpdatePackage{
		Name: "try-omarchy-integration", Version: "4.0.3-7", Architecture: "x86_64",
		Filename: "try-omarchy-integration-4.0.3-7-x86_64.pkg.tar.zst",
		SHA256:   guestPlanTestDigest([]byte("package archive")),
	}
	plan := &guestUpdatePlan{
		Schema: 1,
		Baseline: guestUpdateBaseline{
			Release: "v0.0.20-preview", ReleaseManifestSHA256: strings.Repeat("a", 64),
			ManagedPackages: []guestPackagePrecondition{{Name: pkg.Name, InstalledVersion: "4.0.3-6"}},
		},
		Target: guestUpdateTarget{
			Release: "v0.0.21-preview", Architecture: "x86_64",
			KernelRelease: "7.2.7-arch1-1", CompatRevision: 41,
		},
		Packages: []guestUpdatePackage{pkg},
	}
	manifest := &updateManifest{Version: plan.Target.Release}
	sums := map[string]string{pkg.Filename: pkg.SHA256}
	return plan, sums, manifest
}

func TestGuestUpdatePlanRejectsMalformedAndMismatchedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*guestUpdatePlan, map[string]string, *updateManifest)
	}{
		{"wrong schema", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Schema = 2 }},
		{"wrong release", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Target.Release = "v0.0.22-preview" }},
		{"same baseline", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Baseline.Release = p.Target.Release
		}},
		{"bad baseline digest", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Baseline.ReleaseManifestSHA256 = "missing"
		}},
		{"bad kernel", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Target.KernelRelease = "../../other"
		}},
		{"bad revision", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Target.CompatRevision = 0 }},
		{"unsupported architecture", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Target.Architecture = "other" }},
		{"unshipped guest architecture", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Target.Architecture = "aarch64" }},
		{"path traversal", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Packages[0].Filename = "../" + p.Packages[0].Filename
		}},
		{"filename version mismatch", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Packages[0].Version = "4.0.3-8" }},
		{"bad package digest", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Packages[0].SHA256 = "bad" }},
		{"release digest mismatch", func(_ *guestUpdatePlan, sums map[string]string, _ *updateManifest) {
			for name := range sums {
				sums[name] = strings.Repeat("b", 64)
			}
		}},
		{"missing precondition", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) { p.Baseline.ManagedPackages = nil }},
		{"invalid absent precondition", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			no := false
			p.Baseline.ManagedPackages[0] = guestPackagePrecondition{Name: p.Packages[0].Name, Absent: &no}
		}},
		{"duplicate package", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Packages = append(p.Packages, p.Packages[0])
			p.Baseline.ManagedPackages = append(p.Baseline.ManagedPackages, p.Baseline.ManagedPackages[0])
		}},
		{"duplicate precondition", func(p *guestUpdatePlan, _ map[string]string, _ *updateManifest) {
			p.Baseline.ManagedPackages = append(p.Baseline.ManagedPackages, p.Baseline.ManagedPackages[0])
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, sums, manifest := validGuestUpdatePlanForTest()
			tc.change(plan, sums, manifest)
			if err := validateGuestUpdatePlan(plan, manifest, sums); err == nil {
				t.Fatal("accepted unsafe guest update plan")
			}
		})
	}
	plan, sums, manifest := validGuestUpdatePlanForTest()
	if err := validateGuestUpdatePlan(plan, manifest, sums); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}
	yes := true
	plan.Baseline.ManagedPackages[0] = guestPackagePrecondition{Name: plan.Packages[0].Name, Absent: &yes}
	if err := validateGuestUpdatePlan(plan, manifest, sums); err != nil {
		t.Fatalf("absent-package precondition rejected: %v", err)
	}
}

func TestGuestUpdatePlanRejectsAmbiguousJSONAndSize(t *testing.T) {
	plan, _, _ := validGuestUpdatePlanForTest()
	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"unknown field":   []byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"unexpected":true`, 1)),
		"duplicate field": []byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"schema":1`, 1)),
		"case alias":      []byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"Schema":1`, 1)),
		"unicode alias":   []byte(strings.Replace(string(data), `"schema":1`, `"schema":1,"ſchema":2`, 1)),
		"unicode key":     []byte(strings.Replace(string(data), `"schema":1`, `"ſchema":1`, 1)),
		"null field":      []byte(strings.Replace(string(data), `"schema":1`, `"schema":null`, 1)),
		"trailing object": append(append([]byte(nil), data...), []byte(`{}`)...),
		"oversize":        make([]byte, maxGuestUpdatePlanBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseGuestUpdatePlan(body); err == nil {
				t.Fatal("accepted ambiguous or oversized JSON")
			}
		})
	}
	if _, err := parseGuestUpdatePlan(data); err != nil {
		t.Fatalf("valid JSON rejected: %v", err)
	}
}

type guestPlanRoundTripFunc func(*http.Request) (*http.Response, error)

func (f guestPlanRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGuestUpdatePlanRequiresSignedManifestAndBothReleaseHashes(t *testing.T) {
	for _, tamper := range []string{"none", "signature", "sums", "plan", "package entry"} {
		t.Run(tamper, func(t *testing.T) {
			plan, sums, _ := validGuestUpdatePlanForTest()
			planData, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			sumsText := fmt.Sprintf("%s  %s\n%s  %s\n", guestPlanTestDigest(planData), guestUpdatePlanName,
				plan.Packages[0].SHA256, plan.Packages[0].Filename)
			manifestData := []byte(fmt.Sprintf(`{"schema":1,"version":%q,"release":%q,"manifestSHA256":%q,"launcher":{"name":"TryOmarchy.exe","sha256":%q}}`,
				plan.Target.Release, forkReleaseBase+plan.Target.Release, guestPlanTestDigest([]byte(sumsText)), strings.Repeat("b", 64)))
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			signature := ed25519.Sign(private, manifestData)
			if tamper == "signature" {
				signature[0] ^= 1
			}
			if tamper == "sums" {
				sumsText += "tampered\n"
			}
			if tamper == "plan" {
				planData = append(planData, ' ')
			}
			if tamper == "package entry" {
				sumsText = strings.Replace(sumsText, sums[plan.Packages[0].Filename], strings.Repeat("c", 64), 1)
				// Keep the signed digest valid: this case exercises package/plan
				// binding rather than manifest tampering.
				manifestData = []byte(fmt.Sprintf(`{"schema":1,"version":%q,"release":%q,"manifestSHA256":%q,"launcher":{"name":"TryOmarchy.exe","sha256":%q}}`,
					plan.Target.Release, forkReleaseBase+plan.Target.Release, guestPlanTestDigest([]byte(sumsText)), strings.Repeat("b", 64)))
				signature = ed25519.Sign(private, manifestData)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch {
				case request.URL.Path == "/update.json":
					_, _ = w.Write(manifestData)
				case request.URL.Path == "/update.json.sig":
					_, _ = fmt.Fprintln(w, base64.StdEncoding.EncodeToString(signature))
				case strings.HasSuffix(request.URL.Path, "/SHA256SUMS"):
					_, _ = w.Write([]byte(sumsText))
				case strings.HasSuffix(request.URL.Path, "/"+guestUpdatePlanName):
					_, _ = w.Write(planData)
				default:
					http.NotFound(w, request)
				}
			}))
			defer server.Close()
			client := &http.Client{Transport: guestPlanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				clone := request.Clone(request.Context())
				clone.URL.Scheme = "http"
				clone.URL.Host = strings.TrimPrefix(server.URL, "http://")
				return http.DefaultTransport.RoundTrip(clone)
			})}
			got, err := fetchSignedGuestUpdatePlan(client, server.URL+"/update.json", public)
			if tamper == "none" {
				if err != nil || got == nil || got.Target.Release != plan.Target.Release {
					t.Fatalf("valid signed plan rejected: plan=%v err=%v", got, err)
				}
			} else if err == nil {
				t.Fatal("accepted tampered guest update plan trust chain")
			}
		})
	}
}
