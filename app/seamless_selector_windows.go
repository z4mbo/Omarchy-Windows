//go:build windows

package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	selectorQueueLimit  = 4
	selectorChoiceLimit = 16
	selectorLifetime    = 15 * time.Second
	traySelectorMessage = 0x8004 // WM_APP + 4
	selectorChoiceBase  = 5000
)

type seamlessSelectorTicket struct {
	bridge   *seamlessWindowBridge
	name     string
	previous map[seamlessWindowKey]bool
	until    time.Time
}

type seamlessSelectorQueue struct {
	mu      sync.Mutex
	tickets []seamlessSelectorTicket
	running bool
	posted  bool
	showing bool
}

var windowSelectors seamlessSelectorQueue

func (b *seamlessWindowBridge) selectorPropertyName() string {
	mac := hmac.New(sha256.New, []byte(b.token))
	_, _ = mac.Write([]byte("omarchy-windows-selector-property-v1"))
	return "Omarchy.Windows.Selector." + hex.EncodeToString(mac.Sum(nil)[:16])
}

func (b *seamlessWindowBridge) offerWindowSelector(name string, before []seamlessWindow, now time.Time) bool {
	previous := make(map[seamlessWindowKey]bool, len(before))
	for _, window := range before {
		previous[seamlessKey(window)] = true
	}
	return b.offerWindowSelectorFromKeys(name, previous, now)
}

func (b *seamlessWindowBridge) offerWindowSelectorFromKeys(name string, previous map[seamlessWindowKey]bool, now time.Time) bool {
	if b == nil || name == "" || activeSeamlessBridge.Load() != b {
		return false
	}
	copyPrevious := make(map[seamlessWindowKey]bool, len(previous))
	for key := range previous {
		copyPrevious[key] = true
	}
	if !windowSelectors.enqueue(seamlessSelectorTicket{bridge: b, name: name, previous: copyPrevious, until: now.Add(selectorLifetime)}, now) {
		logf("Windows app selector already pending or full for %s; use Show Windows app in Omarchy from the tray", name)
		return false
	}
	windowSelectors.mu.Lock()
	if !windowSelectors.running {
		windowSelectors.running = true
		go pumpWindowSelectors()
	}
	windowSelectors.mu.Unlock()
	return true
}

func (q *seamlessSelectorQueue) enqueue(ticket seamlessSelectorTicket, now time.Time) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.expireLocked(now)
	for _, existing := range q.tickets {
		if existing.bridge == ticket.bridge && existing.name == ticket.name {
			return false
		}
	}
	if len(q.tickets) >= selectorQueueLimit {
		return false
	}
	q.tickets = append(q.tickets, ticket)
	return true
}

func (q *seamlessSelectorQueue) expireLocked(now time.Time) {
	kept := q.tickets[:0]
	for _, ticket := range q.tickets {
		if now.Before(ticket.until) {
			kept = append(kept, ticket)
		} else {
			logf("Windows app selector expired for %s; use Show Windows app in Omarchy from the tray", ticket.name)
		}
	}
	q.tickets = kept
}

func (q *seamlessSelectorQueue) take(now time.Time) (seamlessSelectorTicket, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.posted = false
	if q.showing {
		return seamlessSelectorTicket{}, false
	}
	q.expireLocked(now)
	if len(q.tickets) == 0 {
		return seamlessSelectorTicket{}, false
	}
	ticket := q.tickets[0]
	q.tickets = q.tickets[1:]
	q.showing = true
	return ticket, true
}

func (q *seamlessSelectorQueue) finish() {
	q.mu.Lock()
	q.showing = false
	q.mu.Unlock()
}

func selectorOmarchyForeground() bool {
	return qemuPid.Load() != 0 && (foregroundPid() == qemuPid.Load() || nativeProjectionForeground())
}

func pumpWindowSelectors() {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	for range ticker.C {
		windowSelectors.mu.Lock()
		windowSelectors.expireLocked(time.Now())
		if len(windowSelectors.tickets) == 0 {
			windowSelectors.running = false
			windowSelectors.posted = false
			windowSelectors.mu.Unlock()
			return
		}
		shouldPost := !windowSelectors.posted && !windowSelectors.showing
		windowSelectors.mu.Unlock()
		if !shouldPost || !selectorOmarchyForeground() {
			continue
		}
		hwnd := trayWindow.Load()
		if hwnd == 0 {
			continue
		}
		windowSelectors.mu.Lock()
		if windowSelectors.posted || windowSelectors.showing || len(windowSelectors.tickets) == 0 {
			windowSelectors.mu.Unlock()
			continue
		}
		windowSelectors.posted = true
		windowSelectors.mu.Unlock()
		if posted, _, _ := procPostMessageW.Call(hwnd, traySelectorMessage, 0, 0); posted == 0 {
			windowSelectors.mu.Lock()
			windowSelectors.posted = false
			windowSelectors.mu.Unlock()
		}
	}
}

// The menu offers every unshared visible host window, with newly appeared
// windows first. Timing only affects order; the user explicitly chooses one.
func selectorChoices(visible, shared []seamlessWindow, previous map[seamlessWindowKey]bool) []seamlessWindow {
	choices, _ := traySeamlessWindowChoices(visible, shared)
	sort.SliceStable(choices, func(i, j int) bool {
		return !previous[seamlessKey(choices[i])] && previous[seamlessKey(choices[j])]
	})
	if len(choices) > selectorChoiceLimit {
		choices = choices[:selectorChoiceLimit]
	}
	return choices
}

func markedSelectorChoices(b *seamlessWindowBridge, choices []seamlessWindow) []seamlessWindow {
	name := b.selectorPropertyName()
	key, _ := syscall.UTF16PtrFromString(name)
	marked := make([]seamlessWindow, 0, len(choices))
	for _, window := range choices {
		if window.created == 0 || window.threadID == 0 || !seamlessWindowStillMatches(window) ||
			nativeWindowProperty(window.handle, name) != 0 {
			continue
		}
		marker, err := randomGrantMarker()
		if err != nil {
			continue
		}
		if ok, _, _ := seamlessSetProp.Call(window.handle, uintptr(unsafe.Pointer(key)), marker); ok == 0 {
			continue
		}
		window.incarnation, window.grantProperty = marker, name
		if !seamlessWindowStillMatches(window) {
			nativeRemoveGrantProperty(window.handle, name, marker)
			continue
		}
		marked = append(marked, window)
	}
	// Refresh the labels after setting identity markers. A window can change
	// title while Explorer or another singleton finishes opening; the menu
	// should describe the exact HWND the user is about to choose.
	current := make(map[seamlessWindowKey]seamlessWindow)
	for _, window := range b.backend.windows() {
		current[seamlessKey(window)] = window
	}
	fresh := marked[:0]
	for _, window := range marked {
		latest, ok := current[seamlessKey(window)]
		if !ok || latest.Class != window.Class || latest.created != window.created ||
			latest.threadID != window.threadID || !seamlessWindowStillMatches(window) {
			nativeRemoveGrantProperty(window.handle, name, window.incarnation)
			continue
		}
		window.Title, window.Process = latest.Title, latest.Process
		fresh = append(fresh, window)
	}
	return fresh
}

func showWindowSelector(owner uintptr) {
	if !selectorOmarchyForeground() {
		windowSelectors.mu.Lock()
		windowSelectors.posted = false
		windowSelectors.mu.Unlock()
		return
	}
	ticket, ok := windowSelectors.take(time.Now())
	if !ok {
		return
	}
	defer windowSelectors.finish()
	if activeSeamlessBridge.Load() != ticket.bridge {
		return
	}
	restore, err := nativeDPIEnter()
	if err != nil {
		logf("Windows app selector unavailable: %v", err)
		return
	}
	defer restore()
	client, err := nativeQemuClientRect()
	if err != nil {
		logf("Windows app selector cannot locate Omarchy: %v", err)
		return
	}
	visible := ticket.bridge.backend.windows()
	shared := ticket.bridge.sharedWindows(visible, time.Now())
	choices := markedSelectorChoices(ticket.bridge, selectorChoices(visible, shared, ticket.previous))
	if len(choices) == 0 {
		logf("Windows app selector found no eligible window for %s; use Show Windows app in Omarchy from the tray", ticket.name)
		return
	}
	defer func() {
		for _, choice := range choices {
			nativeRemoveGrantProperty(choice.handle, choice.grantProperty, choice.incarnation)
		}
	}()
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	appendItem := func(flags, id uintptr, label string) {
		text, _ := syscall.UTF16PtrFromString(label)
		procAppendMenuW.Call(menu, flags, id, uintptr(unsafe.Pointer(text)))
	}
	name := strings.ReplaceAll(ticket.name, "\x00", "")
	if len([]rune(name)) > 48 {
		name = string([]rune(name)[:48]) + "..."
	}
	appendItem(mfString|mfGray, 0, "Choose a Windows window for "+name)
	appendItem(mfSeparator, 0, "")
	for i, choice := range choices {
		label := fmt.Sprintf("%s — %s (PID %d)", choice.Title, choice.Process, choice.PID)
		if len([]rune(label)) > 90 {
			label = string([]rune(label)[:87]) + "..."
		}
		appendItem(mfString, selectorChoiceBase+uintptr(i), label)
	}
	appendItem(mfSeparator, 0, "")
	appendItem(mfString, 0, "Cancel")
	// TrackPopupMenu requires a window owned by this process. The hidden tray
	// HWND owns it, while its position stays inside the verified QEMU client.
	if !selectorOmarchyForeground() {
		return
	}
	procSetForegroundWindow.Call(owner)
	x := client.left + min((client.right-client.left)/4, 100)
	y := client.top + min((client.bottom-client.top)/4, 80)
	command, _, _ := procTrackPopupMenu.Call(menu, tpmRightButton|tpmReturnCmd,
		uintptr(x), uintptr(y), 0, owner, 0)
	procPostMessageW.Call(owner, wmNull, 0, 0)
	if foreground, _, _ := procGetForegroundWindow.Call(); foreground == owner {
		qemu := qemuHwnd.Load()
		if qemu != 0 && isQemuDisplayWindow(qemu, qemuPid.Load()) {
			procSetForegroundWindow.Call(qemu)
		}
	}
	if command < selectorChoiceBase || command >= selectorChoiceBase+uintptr(len(choices)) {
		return
	}
	choice := choices[command-selectorChoiceBase]
	if err := ticket.bridge.grantWindowChecked(choice); err != nil {
		logf("Windows app selector could not grant chosen window for %s: %v", ticket.name, err)
		infoBox("Cannot show that Windows window in Omarchy: " + err.Error())
	}
}
