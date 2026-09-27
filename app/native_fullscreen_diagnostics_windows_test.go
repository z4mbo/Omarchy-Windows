//go:build windows

package main

import (
	"testing"
	"time"
)

func TestNativeFullscreenDiagnosticsRateLimit(t *testing.T) {
	start := time.Now()
	if nativeFullscreenDiagnosticDue("before", "after", start, start.Add(time.Second)) {
		t.Fatal("changed state logged faster than the two-second cap")
	}
	if !nativeFullscreenDiagnosticDue("before", "after", start, start.Add(2*time.Second)) {
		t.Fatal("changed state was not logged")
	}
	if nativeFullscreenDiagnosticDue("same", "same", start, start.Add(29*time.Second)) ||
		!nativeFullscreenDiagnosticDue("same", "same", start, start.Add(30*time.Second)) {
		t.Fatal("steady state did not follow the thirty-second interval")
	}
}

func TestNativeFullscreenDiagnosticGateSurvivesSnapshotReplacementAndStaysBounded(t *testing.T) {
	var limiter nativeFullscreenDiagnosticLimiter
	first := &nativeProjectedWindow{window: seamlessWindow{PID: 25104, handle: 0xc607ac, incarnation: 7}, created: 100}
	replacement := &nativeProjectedWindow{window: first.window, created: first.created}
	grant := nativeFullscreenDiagnosticIdentity(first)
	if nativeFullscreenDiagnosticIdentity(replacement) != grant {
		t.Fatal("replacement snapshot lost the same grant identity")
	}
	start := time.Now()
	if !limiter.allow(grant, "same measurement", start) {
		t.Fatal("first diagnostic was suppressed")
	}
	// A failed placement builds a new nativeProjectedWindow every heartbeat.
	// Its key remains the same, so the projection-level gate must persist.
	for i := 1; i <= 74; i++ {
		if limiter.allow(nativeFullscreenDiagnosticIdentity(replacement), "same measurement",
			start.Add(time.Duration(i)*400*time.Millisecond)) {
			t.Fatalf("snapshot recreation logged the same measurement at heartbeat %d", i)
		}
	}
	if !limiter.allow(grant, "same measurement", start.Add(31*time.Second)) {
		t.Fatal("steady measurement never reached its thirty-second sample")
	}
	if limiter.allow(grant, "changed measurement", start.Add(32*time.Second)) ||
		!limiter.allow(grant, "changed measurement", start.Add(33*time.Second)) {
		t.Fatal("changed measurement ignored its two-second minimum")
	}
	for i := 0; i < nativeFullscreenDiagnosticCap+5; i++ {
		other := nativeFullscreenDiagnosticKey{pid: uint32(30000 + i), hwnd: uintptr(i + 1), created: 200}
		limiter.allow(other, "first", start.Add(34*time.Second+time.Duration(i)*time.Millisecond))
	}
	if len(limiter.records) != nativeFullscreenDiagnosticCap {
		t.Fatal("diagnostic identity cache exceeded its cap")
	}
	limiter.allow(grant, "after expiry", start.Add(34*time.Second+nativeFullscreenDiagnosticTTL))
	if len(limiter.records) != 1 {
		t.Fatal("expired diagnostic identities were retained")
	}
}
