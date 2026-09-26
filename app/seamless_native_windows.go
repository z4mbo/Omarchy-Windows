//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

type nativeSeamlessWindows struct{}

var (
	seamlessEnumMu       sync.Mutex
	seamlessEnumerated   []seamlessWindow
	seamlessEnumCallback = syscall.NewCallback(seamlessEnumWindow)
	seamlessDwm          = syscall.NewLazyDLL("dwmapi.dll")
	seamlessGdi          = syscall.NewLazyDLL("gdi32.dll")
	seamlessGetWindow    = user32.NewProc("GetWindow")
	seamlessIsWindow     = user32.NewProc("IsWindow")
	seamlessIsHung       = user32.NewProc("IsHungAppWindow")
	seamlessGetClass     = user32.NewProc("GetClassNameW")
	seamlessGetRect      = user32.NewProc("GetWindowRect")
	seamlessScreenClient = user32.NewProc("ScreenToClient")
	seamlessPost         = user32.NewProc("PostMessageW")
	seamlessPrint        = user32.NewProc("PrintWindow")
	seamlessMonitor      = user32.NewProc("MonitorFromWindow")
	seamlessMonitorInfo  = user32.NewProc("GetMonitorInfoW")
	seamlessDwmAttr      = seamlessDwm.NewProc("DwmGetWindowAttribute")
	seamlessOpenProcess  = kernel32.NewProc("OpenProcess")
	seamlessProcessName  = kernel32.NewProc("QueryFullProcessImageNameW")
	seamlessCloseHandle  = kernel32.NewProc("CloseHandle")
	seamlessCreateDC     = seamlessGdi.NewProc("CreateCompatibleDC")
	seamlessCreateDIB    = seamlessGdi.NewProc("CreateDIBSection")
	seamlessSelectObject = seamlessGdi.NewProc("SelectObject")
	seamlessDeleteObject = seamlessGdi.NewProc("DeleteObject")
	seamlessDeleteDC     = seamlessGdi.NewProc("DeleteDC")
)

type seamlessRect struct{ left, top, right, bottom int32 }
type seamlessMonitorInfoStruct struct {
	size    uint32
	monitor seamlessRect
	work    seamlessRect
	flags   uint32
}

func (nativeSeamlessWindows) windows() []seamlessWindow {
	// syscall.NewCallback consumes a process-global finite table. The
	// callback is allocated once and this lock serializes its scratch slice.
	seamlessEnumMu.Lock()
	defer seamlessEnumMu.Unlock()
	seamlessEnumerated = nil
	procEnumWindows.Call(seamlessEnumCallback, 0)
	result := seamlessEnumerated
	seamlessEnumerated = nil
	return result
}

func seamlessEnumWindow(hwnd, _ uintptr) uintptr {
	visible, _, _ := procIsWindowVisible.Call(hwnd)
	if visible == 0 {
		return 1
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if pid == 0 || pid == uint32(os.Getpid()) || pid == qemuPid.Load() {
		return 1
	}
	var titleBuf [512]uint16
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&titleBuf[0])), uintptr(len(titleBuf)))
	title := strings.TrimSpace(syscall.UTF16ToString(titleBuf[:]))
	if title == "" {
		return 1
	}
	var classBuf [128]uint16
	seamlessGetClass.Call(hwnd, uintptr(unsafe.Pointer(&classBuf[0])), uintptr(len(classBuf)))
	class := syscall.UTF16ToString(classBuf[:])
	if class == "Shell_TrayWnd" || class == "Shell_SecondaryTrayWnd" || class == "Progman" || class == "WorkerW" {
		return 1
	}
	var cloaked uint32
	if hr, _, _ := seamlessDwmAttr.Call(hwnd, 14, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); hr == 0 && cloaked != 0 {
		return 1
	}
	var rect seamlessRect
	if ok, _, _ := seamlessGetRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return 1
	}
	width, height := rect.right-rect.left, rect.bottom-rect.top
	if width < 64 || height < 48 || width > 8192 || height > 8192 {
		return 1
	}
	process := seamlessProcessBase(pid)
	visibleRect := rect
	_ = seamlessVisibleRect(hwnd, &visibleRect)
	seamlessEnumerated = append(seamlessEnumerated, seamlessWindow{
		HWND:       fmt.Sprintf("%x", hwnd),
		PID:        pid,
		Title:      title,
		Class:      class,
		Process:    process,
		AppGroup:   seamlessAppGroup(process, title),
		X:          rect.left,
		Y:          rect.top,
		Width:      width,
		Height:     height,
		Fullscreen: seamlessCoversMonitor(hwnd, visibleRect),
		handle:     hwnd,
	})
	return 1
}

func seamlessVisibleRect(hwnd uintptr, rect *seamlessRect) bool {
	// DWM's extended frame bounds omit Windows 11's invisible resize border.
	// Use them only for monitor coverage; PrintWindow renders the outer HWND.
	if hr, _, _ := seamlessDwmAttr.Call(hwnd, 9, uintptr(unsafe.Pointer(rect)), unsafe.Sizeof(*rect)); hr == 0 && rect.right > rect.left && rect.bottom > rect.top {
		return true
	}
	if ok, _, _ := seamlessGetRect.Call(hwnd, uintptr(unsafe.Pointer(rect))); ok == 0 {
		return false
	}
	return rect.right > rect.left && rect.bottom > rect.top
}

func seamlessCoversMonitor(hwnd uintptr, rect seamlessRect) bool {
	monitor, _, _ := seamlessMonitor.Call(hwnd, 2) // MONITOR_DEFAULTTONEAREST
	if monitor == 0 {
		return false
	}
	info := seamlessMonitorInfoStruct{size: uint32(unsafe.Sizeof(seamlessMonitorInfoStruct{}))}
	if ok, _, _ := seamlessMonitorInfo.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
		return false
	}
	m := info.monitor
	const tolerance = 2
	return rect.left <= m.left+tolerance && rect.top <= m.top+tolerance &&
		rect.right >= m.right-tolerance && rect.bottom >= m.bottom-tolerance
}

func seamlessProcessBase(pid uint32) string {
	const processQueryLimitedInformation = 0x1000
	handle, _, _ := seamlessOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return ""
	}
	defer seamlessCloseHandle.Call(handle)
	var buf [32768]uint16
	length := uint32(len(buf))
	if ok, _, _ := seamlessProcessName.Call(handle, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&length))); ok == 0 || length == 0 || length > uint32(len(buf)) {
		return ""
	}
	return filepath.Base(syscall.UTF16ToString(buf[:length]))
}

func seamlessWindowStillMatches(window seamlessWindow) bool {
	if ok, _, _ := seamlessIsWindow.Call(window.handle); ok == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(window.handle, uintptr(unsafe.Pointer(&pid)))
	return pid == window.PID
}

func seamlessAppGroup(process, title string) string {
	name := strings.ToLower(strings.TrimSuffix(process, filepath.Ext(process)))
	if strings.Contains(name, "league") || strings.Contains(strings.ToLower(title), "league of legends") {
		return "league"
	}
	if strings.Contains(name, "tftclient") || strings.Contains(strings.ToLower(title), "teamfight tactics") {
		return "tft"
	}
	if name == "" {
		return "unknown"
	}
	return name
}

type seamlessBitmapInfo struct {
	size          uint32
	width, height int32
	planes, bits  uint16
	compression   uint32
	sizeImage     uint32
	xppm, yppm    int32
	used, vital   uint32
}

func (nativeSeamlessWindows) frame(window seamlessWindow) ([]byte, error) {
	if !seamlessWindowStillMatches(window) {
		return nil, errSeamlessGone
	}
	if hung, _, _ := seamlessIsHung.Call(window.handle); hung != 0 {
		return nil, errSeamlessCapture
	}
	var rect seamlessRect
	if ok, _, _ := seamlessGetRect.Call(window.handle, uintptr(unsafe.Pointer(&rect))); ok == 0 {
		return nil, errSeamlessCapture
	}
	width, height := rect.right-rect.left, rect.bottom-rect.top
	if width < 1 || height < 1 || width > 4096 || height > 4096 || int64(width)*int64(height) > 12_000_000 {
		return nil, errSeamlessCapture
	}
	dc, _, _ := seamlessCreateDC.Call(0)
	if dc == 0 {
		return nil, errSeamlessCapture
	}
	defer seamlessDeleteDC.Call(dc)
	info := seamlessBitmapInfo{size: uint32(unsafe.Sizeof(seamlessBitmapInfo{})), width: width, height: -height, planes: 1, bits: 32}
	var pixels uintptr
	bitmap, _, _ := seamlessCreateDIB.Call(dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&pixels)), 0, 0)
	if bitmap == 0 || pixels == 0 {
		return nil, errSeamlessCapture
	}
	defer seamlessDeleteObject.Call(bitmap)
	previous, _, _ := seamlessSelectObject.Call(dc, bitmap)
	defer seamlessSelectObject.Call(dc, previous)
	// PW_RENDERFULLCONTENT requests a full HWND render. It is not a GPU
	// capture API, so black/blank frames are possible for games and video.
	if ok, _, _ := seamlessPrint.Call(window.handle, dc, 2); ok == 0 {
		return nil, errSeamlessCapture
	}
	source := unsafe.Slice((*byte)(unsafe.Pointer(pixels)), int(width*height*4))
	frame := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))
	for i := 0; i < len(source); i += 4 {
		frame.Pix[i] = source[i+2]
		frame.Pix[i+1] = source[i+1]
		frame.Pix[i+2] = source[i]
		frame.Pix[i+3] = 255
	}
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, frame); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (nativeSeamlessWindows) input(window seamlessWindow, input seamlessInput) error {
	if !seamlessWindowStillMatches(window) {
		return errSeamlessGone
	}
	switch input.Type {
	case "pointer":
		// The guest supplies frame-local coordinates. Convert to the HWND's
		// client coordinate system without moving the real host pointer.
		point := struct{ x, y int32 }{window.X + input.X, window.Y + input.Y}
		if ok, _, _ := seamlessScreenClient.Call(window.handle, uintptr(unsafe.Pointer(&point))); ok == 0 {
			return errSeamlessInput
		}
		message, state := uintptr(0x0200), uintptr(0) // WM_MOUSEMOVE
		switch input.Button {
		case 1:
			state = 1
			if input.Down {
				message = 0x0201
			} else {
				message = 0x0202
			}
		case 3:
			state = 2
			if input.Down {
				message = 0x0204
			} else {
				message = 0x0205
			}
		case 2:
			state = 16
			if input.Down {
				message = 0x0207
			} else {
				message = 0x0208
			}
		}
		if !input.Down {
			state = 0
		}
		packed := uintptr(uint16(point.x)) | uintptr(uint16(point.y))<<16
		if ok, _, _ := seamlessPost.Call(window.handle, message, state, packed); ok == 0 {
			return errSeamlessInput
		}
	case "key":
		message := uintptr(0x0101) // WM_KEYUP
		if input.Down {
			message = 0x0100
		} // WM_KEYDOWN
		if ok, _, _ := seamlessPost.Call(window.handle, message, uintptr(input.VK), 0); ok == 0 {
			return errSeamlessInput
		}
	case "text":
		units := syscall.StringToUTF16(input.Text)
		for _, unit := range units[:len(units)-1] {
			if ok, _, _ := seamlessPost.Call(window.handle, 0x0102, uintptr(unit), 0); ok == 0 { // WM_CHAR
				return errSeamlessInput
			}
		}
	default:
		return errSeamlessInput
	}
	return nil
}

func (nativeSeamlessWindows) close(window seamlessWindow) error {
	if !seamlessWindowStillMatches(window) {
		return errSeamlessGone
	}
	if ok, _, _ := seamlessPost.Call(window.handle, 0x0010, 0, 0); ok == 0 { // WM_CLOSE
		return errSeamlessInput
	}
	return nil
}
