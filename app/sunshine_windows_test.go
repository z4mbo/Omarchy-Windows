//go:build windows

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSunshineLocalConfigPreservesOtherSettings(t *testing.T) {
	before := "# preserve this comment\r\nbind_address = 0.0.0.0\r\noutput_name = {primary-display}\r\nbind_address = ::\r\n"
	after := localSunshineConfig(before)
	if !sunshineBoundToLoopback(after) {
		t.Fatalf("config does not bind to loopback: %q", after)
	}
	if !strings.Contains(after, "output_name = {primary-display}") || !strings.Contains(after, "# preserve this comment") {
		t.Fatalf("other configuration was lost: %q", after)
	}
	if strings.Contains(after, "0.0.0.0") || strings.Contains(after, "bind_address = ::") {
		t.Fatalf("public address remained in config: %q", after)
	}
}

func TestSunshineLoopbackRejectsMissingAndDuplicateBinds(t *testing.T) {
	for _, content := range []string{
		"", "# bind_address = 127.0.0.1\n", "bind_address = 0.0.0.0\n",
		"bind_address = 127.0.0.1\nbind_address = 127.0.0.1\n",
	} {
		if sunshineBoundToLoopback(content) {
			t.Errorf("accepted unsafe or ambiguous config %q", content)
		}
	}
	if !sunshineBoundToLoopback("bind_address = 127.0.0.1\noutput_name = primary\n") {
		t.Fatal("rejected a local-only config")
	}
	if got := localSunshineConfig("\ufeffbind_address = 0.0.0.0\n"); !sunshineBoundToLoopback(got) || strings.Contains(got, "0.0.0.0") {
		t.Fatalf("BOM config kept a public bind: %q", got)
	}
}

func TestSunshineInstallerPinAndFirewallExclusion(t *testing.T) {
	if !strings.Contains(sunshineMSIURL, "/v"+sunshineVersion+"/") ||
		!strings.HasSuffix(sunshineMSIURL, "/Sunshine-Windows-AMD64-installer.msi") ||
		!validSHA256(sunshineMSISHA256) {
		t.Fatal("Sunshine installer pin is incomplete")
	}
	args := sunshineMSIArgs(`C:\Omarchy\Sunshine.msi`)
	if strings.Join(args, "|") != `/i|C:\Omarchy\Sunshine.msi|/passive|/norestart|ADDLOCAL=ALL|REMOVE=CM_C_firewall` {
		t.Fatalf("installer options changed: %q", args)
	}
}

func TestSunshineStageVerifiesCopiedBytes(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "cache.msi")
	private := filepath.Join(root, "private")
	if err := os.Mkdir(private, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("pinned Sunshine installer fixture")
	if err := os.WriteFile(cache, content, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	want := hex.EncodeToString(digest[:])
	staged, err := stageVerifiedSunshineMSI(cache, private, want)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(staged) != private {
		t.Fatalf("MSI staged outside protected directory: %s", staged)
	}
	if err := os.WriteFile(cache, []byte("changed after staging"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(staged)
	if err != nil || string(got) != string(content) {
		t.Fatalf("staged MSI changed with cache: %q, %v", got, err)
	}
	if err := os.Remove(staged); err != nil {
		t.Fatal(err)
	}
	if _, err := stageVerifiedSunshineMSI(cache, private, want); err == nil {
		t.Fatal("accepted an MSI that no longer matches the pinned digest")
	}
	entries, err := os.ReadDir(private)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed staging left files behind: %v, %v", entries, err)
	}
}
