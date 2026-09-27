//go:build windows

package main

import (
	"testing"
	"time"
)

func TestSelectorQueueIsBoundedCoalescedAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	bridge := &seamlessWindowBridge{}
	var queue seamlessSelectorQueue
	for i, name := range []string{"Explorer", "Notepad", "Blender", "Terminal"} {
		if !queue.enqueue(seamlessSelectorTicket{bridge: bridge, name: name, until: now.Add(selectorLifetime)}, now) {
			t.Fatalf("request %d was rejected", i)
		}
	}
	if queue.enqueue(seamlessSelectorTicket{bridge: bridge, name: "Explorer", until: now.Add(selectorLifetime)}, now) {
		t.Fatal("duplicate explicit app request was not coalesced")
	}
	if queue.enqueue(seamlessSelectorTicket{bridge: bridge, name: "Fifth", until: now.Add(selectorLifetime)}, now) {
		t.Fatal("selector queue exceeded its bound")
	}
	first, ok := queue.take(now.Add(time.Second))
	if !ok || first.name != "Explorer" {
		t.Fatalf("first ticket = %+v, %t", first, ok)
	}
	if _, ok := queue.take(now.Add(time.Second)); ok {
		t.Fatal("a second selector nested while the first menu was open")
	}
	queue.finish()
	if !queue.enqueue(seamlessSelectorTicket{bridge: bridge, name: "Fifth", until: now.Add(selectorLifetime)}, now) {
		t.Fatal("completed request did not free a queue slot")
	}
	if _, ok := queue.take(now.Add(selectorLifetime)); ok {
		t.Fatal("expired selector request could still open a menu")
	}
}

func TestSelectorChoicesPreferNewButRequireExplicitChoice(t *testing.T) {
	old := seamlessWindow{PID: 10, handle: 10, Title: "Existing Explorer"}
	newWindow := seamlessWindow{PID: 20, handle: 20, Title: "New Explorer"}
	shared := seamlessWindow{PID: 30, handle: 30, Title: "Already in Omarchy"}
	choices := selectorChoices([]seamlessWindow{old, shared, newWindow}, []seamlessWindow{shared},
		map[seamlessWindowKey]bool{seamlessKey(old): true, seamlessKey(shared): true})
	if len(choices) != 2 || seamlessKey(choices[0]) != seamlessKey(newWindow) || seamlessKey(choices[1]) != seamlessKey(old) {
		t.Fatalf("new window should be listed first, while old unshared windows remain selectable: %+v", choices)
	}
	if choices[0].incarnation != 0 || choices[1].incarnation != 0 {
		t.Fatal("choice ordering must not mark or grant a window")
	}
}

func TestExpiredNamedLaunchOffersOneExplicitSelector(t *testing.T) {
	// A launcher process can exit without a directly attributable HWND. The
	// fallback is one host-side user choice, never a guessed automatic grant.
	now := time.Now()
	bridge := &seamlessWindowBridge{backend: &fakeSeamlessBackend{},
		pending: []seamlessLaunch{{pid: 42, until: now.Add(-time.Second), selectorName: "File Explorer"}}}
	activeSeamlessBridge.Store(bridge)
	defer activeSeamlessBridge.CompareAndSwap(bridge, nil)
	windowSelectors.mu.Lock()
	windowSelectors.tickets = nil
	windowSelectors.posted = false
	windowSelectors.mu.Unlock()
	if shared := bridge.sharedWindows(nil, now); len(shared) != 0 {
		t.Fatalf("timeout auto-granted windows: %+v", shared)
	}
	ticket, ok := windowSelectors.take(now)
	if !ok || ticket.bridge != bridge || ticket.name != "File Explorer" || len(bridge.pending) != 0 {
		t.Fatalf("timeout did not produce exactly one selector ticket: %+v, %t", ticket, ok)
	}
	windowSelectors.finish()
	bridge.sharedWindows(nil, now.Add(time.Second))
	if _, ok := windowSelectors.take(now.Add(time.Second)); ok {
		t.Fatal("expired launch queued a second selector request")
	}
}
