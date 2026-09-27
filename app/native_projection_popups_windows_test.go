//go:build windows

package main

import "testing"

func TestNativeOwnedPopupChainRequiresExactSameProcessOwner(t *testing.T) {
	owner, pid := uintptr(0x100), uint32(73)
	direct := []nativeOwnedPopupOwnerLink{{handle: owner, root: owner, pid: pid}}
	nested := []nativeOwnedPopupOwnerLink{
		{handle: 0x200, root: 0x200, pid: pid},
		{handle: owner, root: owner, pid: pid},
	}
	if !nativeOwnedPopupChainValid(direct, owner, pid) || !nativeOwnedPopupChainValid(nested, owner, pid) {
		t.Fatal("valid direct or nested same-process owner chain was rejected")
	}
	for name, chain := range map[string][]nativeOwnedPopupOwnerLink{
		"unrelated owner": {{handle: 0x300, root: 0x300, pid: pid}},
		"foreign middle":  {{handle: 0x200, root: 0x200, pid: 74}, {handle: owner, root: owner, pid: pid}},
		"child middle":    {{handle: 0x200, root: 0x201, pid: pid}, {handle: owner, root: owner, pid: pid}},
		"reused HWND":     {{handle: 0x200, root: 0x200, pid: pid}, {handle: 0x200, root: 0x200, pid: pid}, {handle: owner, root: owner, pid: pid}},
		"owner not last":  {{handle: owner, root: owner, pid: pid}, {handle: 0x200, root: 0x200, pid: pid}},
	} {
		if nativeOwnedPopupChainValid(chain, owner, pid) {
			t.Fatalf("accepted %s chain", name)
		}
	}
	if nativeOwnedPopupChainValid(make([]nativeOwnedPopupOwnerLink, nativeOwnedPopupDepth+1), owner, pid) {
		t.Fatal("accepted unbounded owner chain")
	}
}

func TestNativePopupReconciliationActions(t *testing.T) {
	cases := []struct {
		name                                string
		ownerVisible, tracked, shown, valid bool
		want                                nativePopupVisibilityAction
	}{
		{"hide a new visible popup with its owner", false, false, true, true, nativePopupHide},
		{"hide a tracked popup that became visible again", false, true, true, true, nativePopupHide},
		{"leave an already hidden popup alone", false, true, false, true, nativePopupKeep},
		{"restore a still hidden tracked popup", true, true, false, true, nativePopupShow},
		{"unmark an app shown tracked popup", true, true, true, true, nativePopupUnmark},
		{"leave an untracked visible popup alone", true, false, true, true, nativePopupKeep},
		{"drop a recycled tracked HWND", false, true, true, false, nativePopupDrop},
		{"never adopt an invalid untracked HWND", false, false, true, false, nativePopupKeep},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nativePopupAction(tc.ownerVisible, tc.tracked, tc.shown, tc.valid)
			if got != tc.want {
				t.Fatalf("reconcile action = %d, want %d", got, tc.want)
			}
		})
	}
}
