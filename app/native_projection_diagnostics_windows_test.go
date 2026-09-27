//go:build windows

package main

import (
	"testing"
	"time"
)

func TestNativeZOrderDiagnosticRateLimit(t *testing.T) {
	now := time.Unix(100, 0)
	if !nativeShouldLogZOrderDiagnostic("", time.Time{}, "behind", now) {
		t.Fatal("first failure must be logged")
	}
	if nativeShouldLogZOrderDiagnostic("behind", now, "behind", now.Add(time.Second)) {
		t.Fatal("same failure must be suppressed within the interval")
	}
	if !nativeShouldLogZOrderDiagnostic("behind", now, "SetWindowPos failed", now.Add(time.Second)) {
		t.Fatal("changed failure reason must be logged")
	}
	if !nativeShouldLogZOrderDiagnostic("behind", now, "behind", now.Add(nativeZOrderDiagnosticInterval)) {
		t.Fatal("persistent failure must be logged after the interval")
	}
}
