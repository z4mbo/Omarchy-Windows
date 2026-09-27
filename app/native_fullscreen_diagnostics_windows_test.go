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
