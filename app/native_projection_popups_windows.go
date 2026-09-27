//go:build windows

package main

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"
)

const nativeOwnedPopupLimit = 32
const nativeOwnedPopupDepth = 8

type nativeOwnedPopup struct {
	handle   uintptr
	threadID uint32
	marker   uintptr
}

type nativeOwnedPopupCandidate struct {
	handle   uintptr
	threadID uint32
}

type nativeOwnedPopupOwnerLink struct {
	handle uintptr
	root   uintptr
	pid    uint32
}

type nativePopupVisibilityAction uint8

const (
	nativePopupKeep nativePopupVisibilityAction = iota
	nativePopupDrop
	nativePopupHide
	nativePopupShow
	nativePopupUnmark
)

func nativePopupAction(ownerVisible, tracked, shown, identityValid bool) nativePopupVisibilityAction {
	if !identityValid {
		if tracked {
			return nativePopupDrop
		}
		return nativePopupKeep
	}
	if !ownerVisible {
		if shown {
			return nativePopupHide
		}
		return nativePopupKeep
	}
	if !tracked {
		return nativePopupKeep
	}
	if shown {
		return nativePopupUnmark
	}
	return nativePopupShow
}

type nativeOwnedPopupScan struct {
	owner      uintptr
	pid        uint32
	popups     []nativeOwnedPopupCandidate
	overflowed bool
}

var (
	nativeOwnedPopupEnumMu       sync.Mutex
	nativeOwnedPopupEnumCurrent  *nativeOwnedPopupScan
	nativeOwnedPopupEnumCallback = syscall.NewCallback(nativeCollectOwnedPopup)
)

func nativeOwnedPopupChainValid(chain []nativeOwnedPopupOwnerLink, owner uintptr, pid uint32) bool {
	if len(chain) == 0 || len(chain) > nativeOwnedPopupDepth || owner == 0 || pid == 0 {
		return false
	}
	seen := make(map[uintptr]bool, len(chain))
	for i, link := range chain {
		if link.handle == 0 || link.root != link.handle || link.pid != pid || seen[link.handle] {
			return false
		}
		seen[link.handle] = true
		if link.handle == owner {
			return i == len(chain)-1
		}
	}
	return false
}

// A root-owner match alone is not enough: every intermediate owner must be a
// top-level HWND in the granted app process. The depth bound fails closed.
func nativeOwnedPopupChain(hwnd, owner uintptr, pid uint32) bool {
	var chain [nativeOwnedPopupDepth]nativeOwnedPopupOwnerLink
	for i := 0; i < nativeOwnedPopupDepth; i++ {
		next, _, _ := nativeGetWindow.Call(hwnd, 4) // GW_OWNER
		if next == 0 || next == hwnd {
			return false
		}
		root, _, _ := nativeGetAncestor.Call(next, 2) // GA_ROOT
		var nextPID uint32
		procGetWindowThreadProcessId.Call(next, uintptr(unsafe.Pointer(&nextPID)))
		chain[i] = nativeOwnedPopupOwnerLink{handle: next, root: root, pid: nextPID}
		if next == owner {
			return nativeOwnedPopupChainValid(chain[:i+1], owner, pid)
		}
		hwnd = next
	}
	return false
}

func nativeCollectOwnedPopup(hwnd, _ uintptr) uintptr {
	scan := nativeOwnedPopupEnumCurrent
	if scan == nil || hwnd == scan.owner {
		return 1
	}
	root, _, _ := nativeGetAncestor.Call(hwnd, 2)      // GA_ROOT
	rootOwner, _, _ := nativeGetAncestor.Call(hwnd, 3) // GA_ROOTOWNER
	if root != hwnd || rootOwner != scan.owner {
		return 1
	}
	var pid uint32
	thread, _, _ := procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid != scan.pid || thread == 0 || !nativeOwnedPopupChain(hwnd, scan.owner, pid) {
		return 1
	}
	if len(scan.popups) == nativeOwnedPopupLimit {
		scan.overflowed = true
		return 0
	}
	scan.popups = append(scan.popups, nativeOwnedPopupCandidate{handle: hwnd, threadID: uint32(thread)})
	return 1
}

func nativeEnumerateOwnedPopups(state *nativeProjectedWindow) ([]nativeOwnedPopupCandidate, error) {
	if !nativeIdentityMatches(state) {
		return nil, errors.New("owned popup owner identity changed")
	}
	nativeOwnedPopupEnumMu.Lock()
	defer nativeOwnedPopupEnumMu.Unlock()
	scan := &nativeOwnedPopupScan{owner: state.window.handle, pid: state.window.PID}
	nativeOwnedPopupEnumCurrent = scan
	defer func() { nativeOwnedPopupEnumCurrent = nil }()
	ok, _, callErr := procEnumWindows.Call(nativeOwnedPopupEnumCallback, 0)
	if scan.overflowed {
		return nil, errors.New("too many owned Windows popups to manage safely")
	}
	if ok == 0 {
		return nil, fmt.Errorf("enumerating owned Windows popups: %v", callErr)
	}
	return scan.popups, nil
}

func nativeOwnedPopupMatches(state *nativeProjectedWindow, popup nativeOwnedPopup, property string) bool {
	if popup.handle == 0 || popup.threadID == 0 || popup.marker == 0 || property == "" {
		return false
	}
	root, _, _ := nativeGetAncestor.Call(popup.handle, 2)
	rootOwner, _, _ := nativeGetAncestor.Call(popup.handle, 3)
	var pid uint32
	thread, _, _ := procGetWindowThreadProcessId.Call(popup.handle, uintptr(unsafe.Pointer(&pid)))
	return root == popup.handle && rootOwner == state.window.handle &&
		pid == state.window.PID && uint32(thread) == popup.threadID &&
		nativeOwnedPopupChain(popup.handle, state.window.handle, pid) &&
		nativeWindowProperty(popup.handle, property) == popup.marker
}

func nativeSetOwnedPopupVisible(hwnd uintptr, visible bool) error {
	if hung, _, _ := seamlessIsHung.Call(hwnd); hung != 0 {
		return errors.New("owned popup is unresponsive")
	}
	const common = 0x0010 | 0x0200 | 0x0001 | 0x0002 | 0x0004 // NOACTIVATE | NOOWNERZORDER | NOSIZE | NOMOVE | NOZORDER
	flags := uintptr(common | 0x0080)                         // HIDEWINDOW
	if visible {
		flags = common | 0x0040 // SHOWWINDOW
	}
	if ok, _, callErr := procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0, flags); ok == 0 {
		return fmt.Errorf("owned popup visibility change failed: %v", callErr)
	}
	shown, _, _ := procIsWindowVisible.Call(hwnd)
	if (shown != 0) != visible {
		return errors.New("owned popup ignored requested visibility")
	}
	return nil
}

func (p *nativeProjection) popupPropertyName() string {
	return p.bridge.grantPropertyName() + ".OwnedPopup"
}

// Reconcile only HWNDs owned by the exact granted process. Marking each popup
// before hiding lets lease expiry and revoke restore only our own still-live
// HWNDs, without moving, resizing, or activating them.
func (p *nativeProjection) syncOwnedPopupsLocked(state *nativeProjectedWindow, ownerVisible bool) error {
	if ownerVisible {
		return p.restoreOwnedPopupsLocked(state)
	}
	property := p.popupPropertyName()
	observed, err := nativeEnumerateOwnedPopups(state)
	if err != nil {
		return err
	}
	current := make(map[uintptr]nativeOwnedPopupCandidate, len(observed))
	for _, popup := range observed {
		current[popup.handle] = popup
	}
	for hwnd, popup := range state.ownedPopups {
		candidate, exists := current[hwnd]
		if !exists || candidate.threadID != popup.threadID || !nativeOwnedPopupMatches(state, popup, property) {
			nativeRemoveGrantProperty(hwnd, property, popup.marker)
			delete(state.ownedPopups, hwnd)
		}
	}
	key, err := syscall.UTF16PtrFromString(property)
	if err != nil {
		return err
	}
	for _, candidate := range observed {
		popup, tracked := state.ownedPopups[candidate.handle]
		shown, _, _ := procIsWindowVisible.Call(candidate.handle)
		if nativePopupAction(false, tracked, shown != 0, true) != nativePopupHide {
			continue
		}
		if !tracked {
			if nativeWindowProperty(candidate.handle, property) != 0 {
				return errors.New("owned popup has an unrecognized identity marker")
			}
			marker, err := randomGrantMarker()
			if err != nil {
				return err
			}
			if ok, _, callErr := seamlessSetProp.Call(candidate.handle, uintptr(unsafe.Pointer(key)), marker); ok == 0 {
				return fmt.Errorf("marking owned popup identity: %v", callErr)
			}
			popup = nativeOwnedPopup{handle: candidate.handle, threadID: candidate.threadID, marker: marker}
			if !nativeIdentityMatches(state) || !nativeOwnedPopupMatches(state, popup, property) {
				nativeRemoveGrantProperty(candidate.handle, property, marker)
				return errors.New("owned popup identity changed before hide")
			}
			if state.ownedPopups == nil {
				state.ownedPopups = make(map[uintptr]nativeOwnedPopup)
			}
			state.ownedPopups[candidate.handle] = popup
		}
		// The enumeration and map reconciliation happened earlier. Check the
		// exact marker again immediately before mutating an existing HWND.
		if !nativeIdentityMatches(state) || !nativeOwnedPopupMatches(state, popup, property) {
			return errors.New("owned popup identity changed before hide")
		}
		if err := nativeSetOwnedPopupVisible(candidate.handle, false); err != nil {
			return err // keep its marker and retry or restore on release
		}
		if !nativeIdentityMatches(state) || !nativeOwnedPopupMatches(state, popup, property) {
			return errors.New("owned popup identity changed during hide")
		}
	}
	return nil
}

func (p *nativeProjection) restoreOwnedPopupsLocked(state *nativeProjectedWindow) error {
	if len(state.ownedPopups) == 0 {
		return nil
	}
	if !nativeIdentityMatches(state) {
		return errors.New("owned popup owner identity changed before restore")
	}
	property := p.popupPropertyName()
	for hwnd, popup := range state.ownedPopups {
		valid := nativeOwnedPopupMatches(state, popup, property)
		shown, _, _ := procIsWindowVisible.Call(hwnd)
		action := nativePopupAction(true, true, shown != 0, valid)
		if action == nativePopupDrop {
			nativeRemoveGrantProperty(hwnd, property, popup.marker)
			delete(state.ownedPopups, hwnd)
			continue
		}
		if action == nativePopupShow {
			if !nativeIdentityMatches(state) || !nativeOwnedPopupMatches(state, popup, property) {
				return errors.New("owned popup identity changed before restore")
			}
			if err := nativeSetOwnedPopupVisible(hwnd, true); err != nil {
				return err // retain the marker and retry after a failed restore
			}
			if !nativeIdentityMatches(state) || !nativeOwnedPopupMatches(state, popup, property) {
				return errors.New("owned popup identity changed during restore")
			}
		}
		nativeRemoveGrantProperty(hwnd, property, popup.marker)
		if nativeWindowProperty(hwnd, property) == popup.marker {
			return errors.New("owned popup identity marker could not be cleared")
		}
		delete(state.ownedPopups, hwnd)
	}
	return nil
}

func (p *nativeProjection) dropOwnedPopupsLocked(state *nativeProjectedWindow) {
	property := p.popupPropertyName()
	for hwnd, popup := range state.ownedPopups {
		nativeRemoveGrantProperty(hwnd, property, popup.marker)
		delete(state.ownedPopups, hwnd)
	}
}
