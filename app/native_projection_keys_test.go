package main

import (
	"reflect"
	"testing"
)

func assertNativeKey(t *testing.T, got nativeKeyDecision, swallow bool, events ...nativeQKeyEvent) {
	t.Helper()
	if got.Swallow != swallow || !reflect.DeepEqual(got.Events, events) {
		t.Fatalf("decision = %+v, want swallow=%t events=%+v", got, swallow, events)
	}
}

func TestNativeProjectionRoutesOnlyManagedSuperChord(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step('A', true, true), false)
	assertNativeKey(t, r.Step('A', false, true), false)
	assertNativeKey(t, r.Step(nativeVKAlt, true, true), false)
	assertNativeKey(t, r.Step(0x73, true, true), false) // native Alt+F4
	assertNativeKey(t, r.Step(0x73, false, true), false)
	assertNativeKey(t, r.Step(nativeVKAlt, false, true), false)
	assertNativeKey(t, r.Step(nativeVKLWin, true, false), false)
	assertNativeKey(t, r.Step('1', true, false), false)
	assertNativeKey(t, r.Step('1', false, false), false)
	assertNativeKey(t, r.Step(nativeVKLWin, false, false), false)

	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step('1', true, true), true, nativeQKeyEvent{"1", true})
	assertNativeKey(t, r.Step('1', true, true), true) // autorepeat is not another down
	assertNativeKey(t, r.Step('1', false, true), true, nativeQKeyEvent{"1", false})
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true, nativeQKeyEvent{"meta_l", false})
	assertNativeKey(t, r.Step('A', true, true), false)
}

func TestNativeProjectionUnknownChordCannotTypeIntoGame(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step(0xFF, true, true), true)
	assertNativeKey(t, r.Step(0xFF, true, true), true)
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true, nativeQKeyEvent{"meta_l", false})
	assertNativeKey(t, r.Step(0xFF, false, true), true) // swallowed down retains swallowed up
	assertNativeKey(t, r.Step('Z', true, true), false)
}

func TestNativeProjectionShiftHeldBeforeSuperPassesPhysicalRelease(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLShift, true, true), false)
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true,
		nativeQKeyEvent{"meta_l", true}, nativeQKeyEvent{"shift", true})
	assertNativeKey(t, r.Step('2', true, true), true, nativeQKeyEvent{"2", true})
	assertNativeKey(t, r.Step('2', false, true), true, nativeQKeyEvent{"2", false})
	assertNativeKey(t, r.Step(nativeVKLShift, false, true), false, nativeQKeyEvent{"shift", false})
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true, nativeQKeyEvent{"meta_l", false})
}

func TestNativeProjectionShiftAfterSuperIsSwallowed(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step(nativeVKLShift, true, true), true, nativeQKeyEvent{"shift", true})
	assertNativeKey(t, r.Step('F', true, true), true, nativeQKeyEvent{"f", true})
	assertNativeKey(t, r.Step('F', false, true), true, nativeQKeyEvent{"f", false})
	assertNativeKey(t, r.Step(nativeVKLShift, false, true), true, nativeQKeyEvent{"shift", false})
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true, nativeQKeyEvent{"meta_l", false})
}

func TestNativeProjectionFocusLossReleasesGuestAndConsumedUps(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step('W', true, true), true, nativeQKeyEvent{"w", true})
	assertNativeKey(t, r.Step('A', true, false), false,
		nativeQKeyEvent{"w", false}, nativeQKeyEvent{"meta_l", false})
	assertNativeKey(t, r.Step('A', false, false), false)
	assertNativeKey(t, r.Step('W', false, false), true)
	assertNativeKey(t, r.Step(nativeVKLWin, false, false), true)
	if got := r.Release(); got != nil {
		t.Fatalf("second release = %+v", got)
	}
}

func TestNativeProjectionSuperAltF4DoesNotCloseGame(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step(nativeVKAlt, true, true), true)
	assertNativeKey(t, r.Step(0x73, true, true), true)
	assertNativeKey(t, r.Step(0x73, false, true), true)
	assertNativeKey(t, r.Step(nativeVKAlt, false, true), true)
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true, nativeQKeyEvent{"meta_l", false})
}

func TestNativeProjectionTwoSuperKeysKeepGuestMetaDown(t *testing.T) {
	var r nativeProjectionKeyRouter
	assertNativeKey(t, r.Step(nativeVKLWin, true, true), true, nativeQKeyEvent{"meta_l", true})
	assertNativeKey(t, r.Step(nativeVKRWin, true, true), true)
	assertNativeKey(t, r.Step(nativeVKLWin, false, true), true)
	assertNativeKey(t, r.Step(nativeVKRWin, false, true), true, nativeQKeyEvent{"meta_l", false})
}

func TestNativeProjectionQCodesMatchSupportedChords(t *testing.T) {
	for vk, want := range map[uint32]string{
		'0': "0", '9': "9", 'A': "a", 'Z': "z", nativeVKLeft: "left",
		nativeVKUp: "up", nativeVKReturn: "ret", nativeVKSpace: "spc",
		0x70: "f1", 0x7B: "f12", 0xBA: "semicolon", 0xBF: "slash",
	} {
		if got := nativeChordQCode(vk); got != want {
			t.Errorf("VK %x = %q, want %q", vk, got, want)
		}
	}
}
