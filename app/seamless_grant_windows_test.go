//go:build windows

package main

import (
	"strings"
	"testing"
)

func TestGrantPropertyNameDoesNotExposeBearerToken(t *testing.T) {
	first := &seamlessWindowBridge{token: strings.Repeat("a", 64)}
	second := &seamlessWindowBridge{token: strings.Repeat("b", 64)}
	name := first.grantPropertyName()
	if !strings.HasPrefix(name, "Omarchy.Windows.Grant.") ||
		strings.Contains(name, first.token) ||
		name != first.grantPropertyName() || name == second.grantPropertyName() {
		t.Fatalf("unsafe or unstable property name: %q", name)
	}
}

func TestGrantMetadataRejectsStaleRegrantChoice(t *testing.T) {
	grant := seamlessGrant{class: "Editor", created: 123, threadID: 4, incarnation: 22}
	oldChoice := seamlessWindow{Class: "Editor", created: 123, threadID: 4, incarnation: 11}
	if grantMetadataMatches(oldChoice, grant) {
		t.Fatal("stale granted HWND incarnation matched a newer grant")
	}
	oldChoice.incarnation = 0 // A fresh native enumeration does not yet carry the grant marker.
	if !grantMetadataMatches(oldChoice, grant) {
		t.Fatal("fresh enumeration was rejected before its Win32 property check")
	}
}
