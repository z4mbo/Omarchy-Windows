//go:build windows

package main

import (
	"unsafe"
)

const (
	monitorDefaultToNearest = 2
	enumCurrentSettings     = ^uint32(0)
	monitorInfoPrimary      = 1
)

var (
	procMonitorFromRect      = user32.NewProc("MonitorFromRect")
	procGetMonitorInfoW      = user32.NewProc("GetMonitorInfoW")
	procEnumDisplaySettingsW = user32.NewProc("EnumDisplaySettingsW")
)

type monitorInfoExW struct {
	Size   uint32
	Bounds screenRect
	Work   screenRect
	Flags  uint32
	Device [32]uint16
}

// DEVMODEW is kept complete: EnumDisplaySettingsW writes fields beyond the
// frequency and requires dmSize to describe the whole public structure.
type devModeW struct {
	DeviceName                                                                                     [32]uint16
	SpecVersion, DriverVersion, Size, DriverExtra                                                  uint16
	Fields                                                                                         uint32
	PositionX, PositionY                                                                           int32
	DisplayOrientation, DisplayFixedOutput                                                         uint32
	Color, Duplex, YResolution, TTOption, Collate                                                  uint16
	FormName                                                                                       [32]uint16
	LogPixels                                                                                      uint16
	BitsPerPel, PelsWidth, PelsHeight, DisplayFlags, DisplayFrequency                              uint32
	ICMMethod, ICMIntent, MediaType, DitherType, Reserved1, Reserved2, PanningWidth, PanningHeight uint32
}

type hostMonitorMode struct {
	Bounds, Work   screenRect
	RefreshMilliHz int
}

type launchDisplayMode struct {
	Width, Height  int
	RefreshMilliHz int
}

func currentHostMonitors() []hostMonitorMode {
	rects := monitorRects()
	modes := make([]hostMonitorMode, 0, len(rects))
	for _, rect := range rects {
		mode := hostMonitorMode{Bounds: rect, Work: rect}
		monitor, _, _ := procMonitorFromRect.Call(uintptr(unsafe.Pointer(&rect)), monitorDefaultToNearest)
		if monitor == 0 {
			modes = append(modes, mode)
			continue
		}
		info := monitorInfoExW{Size: uint32(unsafe.Sizeof(monitorInfoExW{}))}
		if ok, _, _ := procGetMonitorInfoW.Call(monitor, uintptr(unsafe.Pointer(&info))); ok == 0 {
			modes = append(modes, mode)
			continue
		}
		mode.Bounds, mode.Work = info.Bounds, info.Work
		var settings devModeW
		settings.Size = uint16(unsafe.Sizeof(settings))
		if ok, _, _ := procEnumDisplaySettingsW.Call(uintptr(unsafe.Pointer(&info.Device[0])), uintptr(enumCurrentSettings), uintptr(unsafe.Pointer(&settings))); ok != 0 {
			// 0 and 1 mean that the display driver did not publish a rate.
			if settings.DisplayFrequency >= 24 && settings.DisplayFrequency <= 1000 {
				mode.RefreshMilliHz = int(settings.DisplayFrequency) * 1000
			}
		}
		modes = append(modes, mode)
	}
	return modes
}

func monitorForPlacement(p *windowPlacement, monitors []hostMonitorMode, fallback int) int {
	if len(monitors) == 0 {
		return -1
	}
	selected := fallback % len(monitors)
	if p == nil {
		return selected
	}
	var best int64
	for i, monitor := range monitors {
		r := p.Normal
		m := monitor.Bounds
		w := max(int64(0), int64(min(r.Right, m.Right)-max(r.Left, m.Left)))
		h := max(int64(0), int64(min(r.Bottom, m.Bottom)-max(r.Top, m.Top)))
		if area := w * h; area > best {
			selected, best = i, area
		}
	}
	return selected
}

// launchDisplayModes matches each virtual output to the Windows monitor on
// which the display enforcer will initially place it. A restored window uses
// its saved monitor. Windowed mode follows the usable SDL client area; only
// fullscreen can have the monitor's full pixel dimensions without scaling.
func launchDisplayModes(dir string, fullscreen bool, count int) []launchDisplayMode {
	monitors := currentHostMonitors()
	if len(monitors) == 0 {
		return nil
	}
	rects := make([]screenRect, len(monitors))
	for i, monitor := range monitors {
		rects[i] = monitor.Bounds
	}
	modes := make([]launchDisplayMode, guestDisplayCount(count))
	for i := range modes {
		index := i % len(monitors)
		var p *windowPlacement
		if !fullscreen {
			if remembered, err := loadDisplayPlacement(dir, i); err == nil && remembered.usable(rects) {
				p = remembered
			}
			index = monitorForPlacement(p, monitors, i)
		}
		monitor := monitors[index]
		mode := launchDisplayMode{RefreshMilliHz: monitor.RefreshMilliHz}
		if fullscreen {
			mode.Width, mode.Height = int(monitor.Bounds.width()), int(monitor.Bounds.height())
		} else if p != nil && !p.Maximized {
			mode.Width, mode.Height = p.consoleSize()
		} else {
			mode.Width, mode.Height = int(monitor.Work.width()), int(monitor.Work.height())-windowTitleBarHeight
		}
		if mode.Width <= 0 || mode.Height <= 0 {
			mode.Width, mode.Height = screenSize(fullscreen)
		}
		modes[i] = mode
	}
	return modes
}
