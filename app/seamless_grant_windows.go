//go:build windows

package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	seamlessSetProp    = user32.NewProc("SetPropW")
	seamlessGetProp    = user32.NewProc("GetPropW")
	seamlessRemoveProp = user32.NewProc("RemovePropW")
)

func (b *seamlessWindowBridge) grantPropertyName() string {
	mac := hmac.New(sha256.New, []byte(b.token))
	_, _ = mac.Write([]byte("omarchy-windows-grant-property-v1"))
	return "Omarchy.Windows.Grant." + hex.EncodeToString(mac.Sum(nil)[:16])
}

func nativeWindowProperty(hwnd uintptr, name string) uintptr {
	key, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0
	}
	value, _, _ := seamlessGetProp.Call(hwnd, uintptr(unsafe.Pointer(key)))
	return value
}

func nativeRemoveGrantProperty(hwnd uintptr, name string, marker uintptr) {
	if marker == 0 || nativeWindowProperty(hwnd, name) != marker {
		return
	}
	key, err := syscall.UTF16PtrFromString(name)
	if err == nil {
		seamlessRemoveProp.Call(hwnd, uintptr(unsafe.Pointer(key)))
		if nativeWindowProperty(hwnd, name) == marker {
			logf("Windows window identity marker could not be removed after grant release")
		}
	}
}

func randomGrantMarker() (uintptr, error) {
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return 0, err
	}
	value := uintptr(binary.LittleEndian.Uint64(bytes[:]))
	if value == 0 {
		return 0, errors.New("empty grant marker")
	}
	return value, nil
}

func (b *seamlessWindowBridge) newGrant(window seamlessWindow) (seamlessGrant, error) {
	grant := seamlessGrant{class: window.Class, created: window.created,
		threadID: window.threadID, incarnation: window.incarnation}
	if _, native := b.backend.(nativeSeamlessWindows); !native {
		return grant, nil
	}
	if window.created == 0 || window.threadID == 0 || !seamlessWindowStillMatches(window) {
		return seamlessGrant{}, errors.New("Windows window process identity could not be verified")
	}
	marker, err := randomGrantMarker()
	if err != nil {
		return seamlessGrant{}, err
	}
	name := b.grantPropertyName()
	if nativeWindowProperty(window.handle, name) != 0 {
		return seamlessGrant{}, errors.New("prior Windows window grant is still restoring")
	}
	key, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return seamlessGrant{}, err
	}
	ok, _, callErr := seamlessSetProp.Call(window.handle, uintptr(unsafe.Pointer(key)), marker)
	if ok == 0 {
		return seamlessGrant{}, fmt.Errorf("cannot mark Windows window identity (possibly elevated): %w", callErr)
	}
	grant.incarnation = marker
	window.incarnation = marker
	window.grantProperty = name
	if !seamlessWindowStillMatches(window) {
		nativeRemoveGrantProperty(window.handle, name, marker)
		return seamlessGrant{}, errors.New("Windows window identity changed during grant")
	}
	return grant, nil
}

func grantMetadataMatches(window seamlessWindow, grant seamlessGrant) bool {
	if grant.class != window.Class || grant.created != window.created || grant.threadID != window.threadID {
		return false
	}
	return window.incarnation == 0 || window.incarnation == grant.incarnation
}

func (b *seamlessWindowBridge) grantMatches(window seamlessWindow, grant seamlessGrant) bool {
	if !grantMetadataMatches(window, grant) ||
		(window.grantProperty != "" && window.grantProperty != b.grantPropertyName()) {
		return false
	}
	if _, native := b.backend.(nativeSeamlessWindows); !native {
		return grant.incarnation == window.incarnation
	}
	if grant.incarnation == 0 {
		return false
	}
	window.incarnation = grant.incarnation
	window.grantProperty = b.grantPropertyName()
	return seamlessWindowStillMatches(window)
}

func (b *seamlessWindowBridge) projectionTracks(key seamlessWindowKey) bool {
	if b.projection == nil {
		return false
	}
	b.projection.mu.Lock()
	_, tracked := b.projection.tracked[key]
	b.projection.mu.Unlock()
	return tracked
}

func (b *seamlessWindowBridge) retireGrant(key seamlessWindowKey, grant seamlessGrant) {
	if _, native := b.backend.(nativeSeamlessWindows); !native || grant.incarnation == 0 {
		return
	}
	if b.projectionTracks(key) {
		b.mu.Lock()
		b.retired = append(b.retired, seamlessRetiredGrant{key: key, grant: grant})
		b.mu.Unlock()
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if current, ok := b.grants[key]; ok && current.incarnation == grant.incarnation {
		return
	}
	nativeRemoveGrantProperty(key.handle, b.grantPropertyName(), grant.incarnation)
}

func (b *seamlessWindowBridge) cleanupRetiredGrants() {
	b.mu.Lock()
	retired := append([]seamlessRetiredGrant(nil), b.retired...)
	b.retired = nil
	b.mu.Unlock()
	for _, entry := range retired {
		b.retireGrant(entry.key, entry.grant)
	}
}

func (b *seamlessWindowBridge) closeGrants() {
	b.mu.Lock()
	grants := b.grants
	b.grants = nil
	retired := b.retired
	b.retired = nil
	b.mu.Unlock()
	for key, grant := range grants {
		b.retireGrant(key, grant)
	}
	for _, entry := range retired {
		b.retireGrant(entry.key, entry.grant)
	}
	b.mu.Lock()
	remaining := len(b.retired)
	b.mu.Unlock()
	if remaining != 0 {
		logf("Windows window identity marker retained for %d window(s) pending placement restoration", remaining)
	}
}
