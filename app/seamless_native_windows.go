//go:build windows

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
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
	seamlessIsChild      = user32.NewProc("IsChild")
	seamlessIsEnabled    = user32.NewProc("IsWindowEnabled")
	seamlessGUIThread    = user32.NewProc("GetGUIThreadInfo")
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
	seamlessInputMu      sync.Mutex
	seamlessSelected     = make(map[uintptr]seamlessControlSelection)
)

type seamlessRect struct{ left, top, right, bottom int32 }
type seamlessPoint struct{ x, y int32 }
type seamlessControlSelection struct {
	pid    uint32
	target uintptr
	button int
}
type seamlessGUIThreadInfo struct {
	size                       uint32
	active, focus, capture     uintptr
	menuOwner, moveSize, caret uintptr
	caretRect                  seamlessRect
}
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
	if frame, err := seamlessWGCFrame(window); err == nil {
		var visible seamlessRect
		if seamlessVisibleRect(window.handle, &visible) {
			if aligned, err := seamlessAlignWGCFrame(frame, rect, visible); err == nil {
				return aligned, nil
			}
		}
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

// WGC can omit part of the invisible resize border in GetWindowRect. The guest
// sends pointer coordinates in PNG pixels; pad the captured frame so every
// pixel uses the same window-relative origin as the input route. Unknown
// geometry falls back to PrintWindow instead of silently moving clicks.
func seamlessAlignWGCFrame(frame []byte, outer, visible seamlessRect) ([]byte, error) {
	outerWidth, outerHeight := int(outer.right-outer.left), int(outer.bottom-outer.top)
	if outerWidth <= 0 || outerHeight <= 0 || outerWidth > 4096 || outerHeight > 4096 ||
		int64(outerWidth)*int64(outerHeight) > 12_000_000 {
		return nil, errSeamlessCapture
	}
	config, err := png.DecodeConfig(bytes.NewReader(frame))
	if err != nil {
		return nil, errSeamlessCapture
	}
	if config.Width == outerWidth && config.Height == outerHeight {
		return frame, nil
	}
	left, top := 0, 0
	if visible.left >= outer.left && visible.top >= outer.top && visible.right <= outer.right && visible.bottom <= outer.bottom &&
		config.Width == int(visible.right-visible.left) && config.Height == int(visible.bottom-visible.top) {
		left, top = int(visible.left-outer.left), int(visible.top-outer.top)
	} else {
		// On some HWNDs the WGC item includes a narrow composited rim that
		// DWM's extended frame bounds exclude. It is centered within the
		// outer rectangle. Limit this case to a few pixels so a resized or
		// unrelated frame cannot silently move clicks across controls.
		dx, dy := outerWidth-config.Width, outerHeight-config.Height
		if dx < 0 || dy < 0 || dx > 8 || dy > 8 {
			return nil, errSeamlessCapture
		}
		left, top = dx/2, dy/2
	}
	source, err := png.Decode(bytes.NewReader(frame))
	if err != nil {
		return nil, errSeamlessCapture
	}
	aligned := image.NewRGBA(image.Rect(0, 0, outerWidth, outerHeight))
	draw.Draw(aligned, image.Rect(left, top, left+config.Width, top+config.Height), source, source.Bounds().Min, draw.Src)
	var output bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&output, aligned); err != nil {
		return nil, errSeamlessCapture
	}
	return output.Bytes(), nil
}

// A target must remain inside the selected top-level window and its process.
// Child HWNDs can disappear or be reused between the hit test and input.
func seamlessValidInputTarget(window seamlessWindow, target uintptr) bool {
	if target == 0 {
		return false
	}
	if ok, _, _ := seamlessIsWindow.Call(target); ok == 0 {
		return false
	}
	var pid uint32
	procGetWindowThreadProcessId.Call(target, uintptr(unsafe.Pointer(&pid)))
	if pid != window.PID {
		return false
	}
	if target == window.handle {
		return true
	}
	ok, _, _ := seamlessIsChild.Call(window.handle, target)
	return ok != 0
}

func seamlessClientPoint(hwnd uintptr, screen seamlessPoint) (seamlessPoint, bool) {
	point := screen
	ok, _, _ := seamlessScreenClient.Call(hwnd, uintptr(unsafe.Pointer(&point)))
	return point, ok != 0
}

// Walk visible, enabled children in Z order. Both depth and sibling work are
// bounded because an application can create an arbitrary number of HWNDs.
func seamlessPointerTarget(window seamlessWindow, screen seamlessPoint) uintptr {
	parent := window.handle
	remaining := 256
	for depth := 0; depth < 16 && remaining > 0; depth++ {
		child, _, _ := seamlessGetWindow.Call(parent, 5) // GW_CHILD
		var hit uintptr
		for child != 0 && remaining > 0 {
			remaining--
			if seamlessValidInputTarget(window, child) {
				visible, _, _ := procIsWindowVisible.Call(child)
				enabled, _, _ := seamlessIsEnabled.Call(child)
				var rect seamlessRect
				if visible != 0 && enabled != 0 {
					if ok, _, _ := seamlessGetRect.Call(child, uintptr(unsafe.Pointer(&rect))); ok != 0 &&
						screen.x >= rect.left && screen.x < rect.right && screen.y >= rect.top && screen.y < rect.bottom {
						hit = child
						break
					}
				}
			}
			child, _, _ = seamlessGetWindow.Call(child, 2) // GW_HWNDNEXT
		}
		if hit == 0 {
			break
		}
		parent = hit
	}
	return parent
}

func seamlessRememberPointer(window seamlessWindow, target uintptr, button int, down bool) {
	seamlessInputMu.Lock()
	defer seamlessInputMu.Unlock()
	if len(seamlessSelected) >= 128 {
		clear(seamlessSelected)
	}
	if down {
		seamlessSelected[window.handle] = seamlessControlSelection{pid: window.PID, target: target, button: button}
	} else if selection, ok := seamlessSelected[window.handle]; ok && selection.pid == window.PID && selection.button == button {
		selection.button = 0
		seamlessSelected[window.handle] = selection
	}
}

func seamlessSelectedTarget(window seamlessWindow) seamlessControlSelection {
	seamlessInputMu.Lock()
	selection := seamlessSelected[window.handle]
	seamlessInputMu.Unlock()
	if selection.pid != window.PID || !seamlessValidInputTarget(window, selection.target) {
		return seamlessControlSelection{}
	}
	return selection
}

func seamlessKeyboardTarget(window seamlessWindow) uintptr {
	// A posted click does not necessarily change the host thread's foreground
	// focus, so prefer the last clicked child in this guest surface.
	if selected := seamlessSelectedTarget(window); selected.target != 0 {
		return selected.target
	}
	thread, _, _ := procGetWindowThreadProcessId.Call(window.handle, 0)
	if thread != 0 {
		info := seamlessGUIThreadInfo{size: uint32(unsafe.Sizeof(seamlessGUIThreadInfo{}))}
		if ok, _, _ := seamlessGUIThread.Call(thread, uintptr(unsafe.Pointer(&info))); ok != 0 && seamlessValidInputTarget(window, info.focus) {
			return info.focus
		}
	}
	return window.handle
}

func (nativeSeamlessWindows) input(window seamlessWindow, input seamlessInput) error {
	if !seamlessWindowStillMatches(window) {
		return errSeamlessGone
	}
	switch input.Type {
	case "pointer":
		// The guest supplies frame-local coordinates. Convert to the HWND's
		// client coordinate system without moving the real host pointer.
		x, y := int64(window.X)+int64(input.X), int64(window.Y)+int64(input.Y)
		if x < -1<<31 || x > 1<<31-1 || y < -1<<31 || y > 1<<31-1 {
			return errSeamlessInput
		}
		screen := seamlessPoint{int32(x), int32(y)}
		target := seamlessPointerTarget(window, screen)
		selected := seamlessSelectedTarget(window)
		if selected.button != 0 && (input.Button == 0 || input.Button == selected.button && !input.Down) {
			target = selected.target // Keep mouse-up and drag on the pressed control.
		}
		if !seamlessValidInputTarget(window, target) {
			return errSeamlessInput
		}
		point, ok := seamlessClientPoint(target, screen)
		if !ok {
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
		if input.Button == 0 && selected.button != 0 {
			switch selected.button {
			case 1:
				state = 1
			case 3:
				state = 2
			case 2:
				state = 16
			}
		}
		if input.Button != 0 && !input.Down {
			state = 0
		}
		packed := uintptr(uint16(point.x)) | uintptr(uint16(point.y))<<16
		if ok, _, _ := seamlessPost.Call(target, message, state, packed); ok == 0 {
			return errSeamlessInput
		}
		if input.Button != 0 {
			seamlessRememberPointer(window, target, input.Button, input.Down)
		}
	case "key":
		message := uintptr(0x0101) // WM_KEYUP
		if input.Down {
			message = 0x0100
		} // WM_KEYDOWN
		target := seamlessKeyboardTarget(window)
		if !seamlessValidInputTarget(window, target) {
			return errSeamlessInput
		}
		if ok, _, _ := seamlessPost.Call(target, message, uintptr(input.VK), 0); ok == 0 {
			return errSeamlessInput
		}
	case "text":
		units := syscall.StringToUTF16(input.Text)
		target := seamlessKeyboardTarget(window)
		for _, unit := range units[:len(units)-1] {
			if !seamlessValidInputTarget(window, target) {
				return errSeamlessInput
			}
			if ok, _, _ := seamlessPost.Call(target, 0x0102, uintptr(unit), 0); ok == 0 { // WM_CHAR
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
