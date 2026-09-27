//go:build windows

package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"
)

// Explorer normally hands ShellExecute requests to its existing process. A
// process handle therefore cannot identify the requested Explorer window.
// ShellBrowserWindow instead gives us the exact IWebBrowser2 for a newly
// created window; the guest still receives only the bridge's opaque grant.
var (
	explorerCLSID         = comGUID{0xc08afd90, 0xf2a1, 0x11d1, [8]byte{0x84, 0x55, 0x00, 0xa0, 0xc9, 0x1f, 0x38, 0x80}}
	explorerIID           = comGUID{0xd30c1661, 0xcdaf, 0x11d0, [8]byte{0x8a, 0x3e, 0x00, 0xc0, 0x4f, 0xc9, 0xe2, 0x6e}}
	explorerCoCreate      = ole32.NewProc("CoCreateInstance")
	explorerParseName     = shell32.NewProc("SHParseDisplayName")
	explorerILGetSize     = shell32.NewProc("ILGetSize")
	explorerVariantBuffer = syscall.NewLazyDLL("propsys.dll").NewProc("InitVariantFromBuffer")
	explorerVariantClear  = syscall.NewLazyDLL("oleaut32.dll").NewProc("VariantClear")
	explorerEnumMu        sync.Mutex
	explorerEnumerated    []uintptr
	explorerEnumCallback  = syscall.NewCallback(explorerRecordTopLevel)
	explorerLaunchGate    explorerFlight
)

const explorerHomeName = `shell:::{f874310e-b6b7-47dc-bc84-b9e6b38f5903}`

const explorerLaunchLifetime = 12 * time.Second

type explorerFlight struct{ active atomic.Bool }

func (f *explorerFlight) start() bool { return f.active.CompareAndSwap(false, true) }
func (f *explorerFlight) done()       { f.active.Store(false) }

var errExplorerPending = errors.New("Explorer window has not appeared in the visible catalogue")

func explorerRecordTopLevel(hwnd, _ uintptr) uintptr {
	explorerEnumerated = append(explorerEnumerated, hwnd)
	return 1
}

// The prelaunch snapshot includes hidden top-level windows as well as visible
// ones. Reopening an existing Explorer tab/window must never auto-grant it.
func explorerTopLevelBefore() (map[uintptr]bool, error) {
	explorerEnumMu.Lock()
	defer explorerEnumMu.Unlock()
	explorerEnumerated = nil
	ok, _, err := procEnumWindows.Call(explorerEnumCallback, 0)
	if ok == 0 {
		return nil, fmt.Errorf("EnumWindows: %w", err)
	}
	before := make(map[uintptr]bool, len(explorerEnumerated))
	for _, hwnd := range explorerEnumerated {
		before[hwnd] = true
	}
	explorerEnumerated = nil
	return before, nil
}

// A COM HWND alone is insufficient: it must be a new, visible Explorer main
// window in the current host catalogue. If two Explorer windows appeared
// concurrently, neither is silently adopted.
func explorerCandidate(before map[uintptr]bool, visible []seamlessWindow, hwnd uintptr) (seamlessWindow, error) {
	if hwnd == 0 || before[hwnd] {
		return seamlessWindow{}, errors.New("Explorer reused an existing window")
	}
	var matches []seamlessWindow
	for _, window := range visible {
		if before[window.handle] || !strings.EqualFold(window.Process, "explorer.exe") {
			continue
		}
		matches = append(matches, window)
	}
	if len(matches) == 0 {
		return seamlessWindow{}, errExplorerPending
	}
	if len(matches) != 1 || matches[0].handle != hwnd {
		return seamlessWindow{}, errors.New("Explorer window attribution is ambiguous")
	}
	window := matches[0]
	if window.Class != "CabinetWClass" && window.Class != "ExploreWClass" {
		return seamlessWindow{}, errors.New("Explorer did not expose a main window")
	}
	if window.PID == 0 || window.threadID == 0 || window.created == 0 {
		return seamlessWindow{}, errors.New("Explorer window identity is incomplete")
	}
	return window, nil
}

func explorerCOMWindowHandle(browser uintptr) (uintptr, error) {
	var hwnd int64 // IWebBrowserApp::get_HWND returns SHANDLE_PTR.
	hr := int32(uint32(comCall(browser, 37, uintptr(unsafe.Pointer(&hwnd)))))
	if hr < 0 {
		return 0, fmt.Errorf("IWebBrowser2.get_HWND: %#x", uint32(hr))
	}
	return uintptr(hwnd), nil
}

func explorerCOMNavigationReady(browser uintptr) (bool, error) {
	var busy int16
	hr := int32(uint32(comCall(browser, 31, uintptr(unsafe.Pointer(&busy))))) // get_Busy
	if hr < 0 {
		return false, fmt.Errorf("IWebBrowser2.get_Busy: %#x", uint32(hr))
	}
	var state int32
	hr = int32(uint32(comCall(browser, 56, uintptr(unsafe.Pointer(&state))))) // get_ReadyState
	if hr < 0 {
		return false, fmt.Errorf("IWebBrowser2.get_ReadyState: %#x", uint32(hr))
	}
	return busy == 0 && state == 4, nil // READYSTATE_COMPLETE on this exact browser object.
}

func explorerHomeVariant() (propVariant, error) {
	var result propVariant
	name, err := syscall.UTF16PtrFromString(explorerHomeName)
	if err != nil {
		return result, err
	}
	var pidl uintptr
	hr, _, _ := explorerParseName.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&pidl)), 0, 0)
	runtime.KeepAlive(name)
	if int32(uint32(hr)) < 0 || pidl == 0 {
		return result, fmt.Errorf("SHParseDisplayName(Home): %#x", uint32(hr))
	}
	defer procCoTaskMemFree.Call(pidl)
	size, _, _ := explorerILGetSize.Call(pidl)
	if size < 2 || size > 65536 {
		return result, errors.New("Home PIDL has invalid size")
	}
	hr, _, _ = explorerVariantBuffer.Call(pidl, size, uintptr(unsafe.Pointer(&result)))
	if int32(uint32(hr)) < 0 || result.vt != 0x2011 { // VT_ARRAY | VT_UI1
		if result.vt != 0 {
			explorerVariantClear.Call(uintptr(unsafe.Pointer(&result)))
		}
		return result, fmt.Errorf("InitVariantFromBuffer(Home): %#x", uint32(hr))
	}
	return result, nil
}

func shellOpenExplorerWindowApp() error {
	bridge := activeSeamlessBridge.Load()
	if bridge == nil {
		return errors.New("Windows app bridge is unavailable")
	}
	if !explorerLaunchGate.start() {
		return errors.New("File Explorer is already opening in Omarchy")
	}
	until := time.Now().Add(explorerLaunchLifetime)
	before, err := explorerTopLevelBefore()
	if err != nil {
		explorerLaunchGate.done()
		return err
	}
	beforeVisible := bridge.backend.windows()
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		explorerLaunchGate.done()
		return errors.New("File Explorer launch expired before activation")
	}
	ready := make(chan error, 1)
	go func() {
		defer explorerLaunchGate.done()
		explorerOpenAndGrant(bridge, before, beforeVisible, until, ready)
	}()
	select {
	case err := <-ready:
		return err
	case <-time.After(2 * time.Second):
		// The guest polls grants independently. A slow Shell COM activation may
		// finish after the bridge has answered; failure then stays ungranted.
		return nil
	}
}

func explorerOpenAndGrant(bridge *seamlessWindowBridge, before map[uintptr]bool, beforeVisible []seamlessWindow, until time.Time, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	responded := false
	respond := func(err error) {
		if !responded {
			ready <- err
			responded = true
		}
		if err != nil {
			logf("Explorer automation: %v", err)
		}
	}
	defer func() {
		if !responded {
			respond(errors.New("Explorer automation did not start"))
		}
	}()
	hr, _, _ := procCoInitializeEx.Call(0, 2|4) // STA, disable OLE 1 DDE
	if int32(uint32(hr)) < 0 {
		respond(fmt.Errorf("Explorer COM initialization: %#x", uint32(hr)))
		return
	}
	defer procCoUninitialize.Call()
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		respond(errors.New("Explorer launch expired before activation"))
		return
	}
	var browser uintptr
	hr, _, _ = explorerCoCreate.Call(uintptr(unsafe.Pointer(&explorerCLSID)), 0, 4,
		uintptr(unsafe.Pointer(&explorerIID)), uintptr(unsafe.Pointer(&browser))) // CLSCTX_LOCAL_SERVER
	if int32(uint32(hr)) < 0 || browser == 0 {
		respond(fmt.Errorf("ShellBrowserWindow activation: %#x", uint32(hr)))
		return
	}
	defer comCall(browser, 2) // Release on the creating STA.
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		respond(errors.New("Explorer launch expired after COM activation"))
		return
	}
	home, err := explorerHomeVariant()
	if err != nil {
		respond(err)
		return
	}
	defer explorerVariantClear.Call(uintptr(unsafe.Pointer(&home)))
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		respond(errors.New("Explorer launch expired before Home navigation"))
		return
	}
	var empty propVariant
	hr = comCall(browser, 52, uintptr(unsafe.Pointer(&home)), uintptr(unsafe.Pointer(&empty)),
		uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty)), uintptr(unsafe.Pointer(&empty))) // Navigate2
	if int32(uint32(hr)) < 0 {
		respond(fmt.Errorf("Explorer Navigate2(Home): %#x", uint32(hr)))
		return
	}
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		respond(errors.New("Explorer launch expired during Home navigation"))
		return
	}
	hr = comCall(browser, 41, uintptr(uint16(0xffff))) // put_Visible(VARIANT_TRUE)
	if int32(uint32(hr)) < 0 {
		respond(fmt.Errorf("Explorer put_Visible: %#x", uint32(hr)))
		return
	}
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		respond(errors.New("Explorer launch expired while showing the window"))
		return
	}
	respond(nil)
	for time.Now().Before(until) && activeSeamlessBridge.Load() == bridge {
		ready, err := explorerCOMNavigationReady(browser)
		if err != nil {
			logf("Explorer Home navigation readiness could not be read: %v", err)
			break
		}
		if !ready {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		hwnd, err := explorerCOMWindowHandle(browser)
		if err != nil {
			break
		}
		if hwnd == 0 {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		window, err := explorerCandidate(before, bridge.backend.windows(), hwnd)
		if err != nil {
			if !errors.Is(err, errExplorerPending) {
				break
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		choices := markedSelectorChoices(bridge, []seamlessWindow{window})
		if len(choices) != 1 {
			break
		}
		choice := choices[0]
		defer nativeRemoveGrantProperty(choice.handle, choice.grantProperty, choice.incarnation)
		if latestHWND, err := explorerCOMWindowHandle(browser); err != nil || latestHWND != choice.handle ||
			!seamlessWindowStillMatches(choice) || !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
			break
		}
		if err := bridge.grantWindowChecked(choice); err != nil {
			logf("Explorer window could not be granted: %v", err)
			break
		}
		logf("Explorer Home window granted by exact ShellBrowserWindow identity")
		return
	}
	if !time.Now().Before(until) || activeSeamlessBridge.Load() != bridge {
		logf("Explorer window attribution expired without an automatic grant")
		return
	}
	if bridge.offerWindowSelector("File Explorer", beforeVisible, time.Now()) {
		logf("Explorer window could not be attributed automatically; offered the explicit window selector")
	} else {
		logf("Explorer window could not be attributed automatically; use Show Windows app in Omarchy from the tray")
	}
}
