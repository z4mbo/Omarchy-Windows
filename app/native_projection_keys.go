package main

import "strconv"

// nativeProjectionKeyRouter only decides which physical key transitions to
// swallow while a managed native window is foreground. The caller owns HWND
// validation, the hook, and delivery of the ordered QMP events.
type nativeProjectionKeyRouter struct {
	active    bool
	shift     bool
	alt       bool
	ctrl      bool
	superHeld int
	held      []nativeRoutedKey
	consumed  map[uint32]bool // physical ups stay swallowed after guest release
}

type nativeRoutedKey struct {
	vk     uint32
	qcode  string
	copied bool // modifier down reached Windows before Super was pressed
}

type nativeQKeyEvent struct {
	QCode string
	Down  bool
}

type nativeKeyDecision struct {
	Swallow bool
	Events  []nativeQKeyEvent
}

const (
	nativeVKShift  = 0x10
	nativeVKCtrl   = 0x11
	nativeVKAlt    = 0x12
	nativeVKReturn = 0x0D
	nativeVKSpace  = 0x20
	nativeVKLeft   = 0x25
	nativeVKUp     = 0x26
	nativeVKRight  = 0x27
	nativeVKDown   = 0x28
	nativeVKLWin   = 0x5B
	nativeVKRWin   = 0x5C
	nativeVKLShift = 0xA0
	nativeVKRShift = 0xA1
	nativeVKLCTRL  = 0xA2
	nativeVKRCTRL  = 0xA3
	nativeVKLAlt   = 0xA4
	nativeVKRAlt   = 0xA5
)

func nativeIsShift(vk uint32) bool {
	return vk == nativeVKShift || vk == nativeVKLShift || vk == nativeVKRShift
}

func nativeChordQCode(vk uint32) string {
	if vk >= '0' && vk <= '9' {
		return string(rune(vk))
	}
	if vk >= 'A' && vk <= 'Z' {
		return string(rune(vk + ('a' - 'A')))
	}
	if vk >= 0x70 && vk <= 0x7B { // F1 through F12
		return "f" + strconv.Itoa(int(vk-0x70+1))
	}
	switch vk {
	case nativeVKLeft:
		return "left"
	case nativeVKUp:
		return "up"
	case nativeVKRight:
		return "right"
	case nativeVKDown:
		return "down"
	case nativeVKReturn:
		return "ret"
	case nativeVKSpace:
		return "spc"
	case 0x09:
		return "tab"
	case 0x08:
		return "backspace"
	case 0x1B:
		return "esc"
	case 0xBD:
		return "minus"
	case 0xBB:
		return "equal"
	case 0xDB:
		return "bracket_left"
	case 0xDD:
		return "bracket_right"
	case 0xBA:
		return "semicolon"
	case 0xDE:
		return "apostrophe"
	case 0xC0:
		return "grave_accent"
	case 0xDC:
		return "backslash"
	case 0xBC:
		return "comma"
	case 0xBE:
		return "dot"
	case 0xBF:
		return "slash"
	}
	return ""
}

func (r *nativeProjectionKeyRouter) index(vk uint32) int {
	for i := range r.held {
		if r.held[i].vk == vk || (nativeIsShift(vk) && nativeIsShift(r.held[i].vk)) {
			return i
		}
	}
	return -1
}

func (r *nativeProjectionKeyRouter) releaseHeldAt(i int) nativeQKeyEvent {
	key := r.held[i]
	r.held = append(r.held[:i], r.held[i+1:]...)
	return nativeQKeyEvent{QCode: key.qcode}
}

// Release must also be called on focus change without a key event, projection
// revoke, and hook teardown. Physical ups for consumed downs remain swallowed.
func (r *nativeProjectionKeyRouter) Release() []nativeQKeyEvent {
	if !r.active {
		return nil
	}
	events := make([]nativeQKeyEvent, 0, len(r.held)+1)
	for i := len(r.held) - 1; i >= 0; i-- {
		events = append(events, nativeQKeyEvent{QCode: r.held[i].qcode})
	}
	r.held = nil
	r.active = false
	r.superHeld = 0
	events = append(events, nativeQKeyEvent{QCode: "meta_l"})
	return events
}

// Step takes one physical key transition. The caller should feed every key,
// including unrelated foreground periods, to track pre-held modifiers.
func (r *nativeProjectionKeyRouter) Step(vk uint32, down, managedForeground bool) nativeKeyDecision {
	var out nativeKeyDecision
	if !managedForeground {
		out.Events = r.Release()
	}
	switch vk {
	case nativeVKShift, nativeVKLShift, nativeVKRShift:
		r.shift = down
	case nativeVKCtrl, nativeVKLCTRL, nativeVKRCTRL:
		r.ctrl = down
	case nativeVKAlt, nativeVKLAlt, nativeVKRAlt:
		r.alt = down
	}
	if r.consumed[vk] {
		out.Swallow = true
		if !down {
			delete(r.consumed, vk)
			if vk == nativeVKLWin || vk == nativeVKRWin {
				if r.active {
					r.superHeld--
					if r.superHeld == 0 {
						out.Events = append(out.Events, r.Release()...)
					}
				}
			} else if r.active {
				if i := r.index(vk); i >= 0 {
					out.Events = append(out.Events, r.releaseHeldAt(i))
				}
			}
		}
		return out
	}
	if vk == nativeVKLWin || vk == nativeVKRWin {
		if down && managedForeground {
			if r.consumed == nil {
				r.consumed = make(map[uint32]bool)
			}
			r.consumed[vk] = true
			r.superHeld++
			out.Swallow = true
			if !r.active {
				r.active = true
				out.Events = append(out.Events, nativeQKeyEvent{QCode: "meta_l", Down: true})
				if r.shift {
					r.held = append(r.held, nativeRoutedKey{vk: nativeVKShift, qcode: "shift", copied: true})
					out.Events = append(out.Events, nativeQKeyEvent{QCode: "shift", Down: true})
				}
			}
		}
		return out
	}
	if !r.active || !managedForeground {
		return out
	}
	// A Shift down before Super already reached Windows. Its up must also
	// reach Windows, while the copied guest Shift is released.
	if nativeIsShift(vk) && !down {
		if i := r.index(vk); i >= 0 && r.held[i].copied {
			out.Events = append(out.Events, r.releaseHeldAt(i))
			return out
		}
	}
	if !down {
		return out
	} // its down was not swallowed
	// Once Super is consumed, unsupported chords cannot type unmodified keys
	// into the game. Alt+F4 passes natively only when no Super was consumed.
	out.Swallow = true
	if r.consumed == nil {
		r.consumed = make(map[uint32]bool)
	}
	r.consumed[vk] = true
	if r.alt || r.ctrl {
		return out
	}
	qcode := nativeChordQCode(vk)
	if nativeIsShift(vk) {
		qcode = "shift"
	}
	if qcode != "" && len(r.held) < 32 && r.index(vk) < 0 {
		r.held = append(r.held, nativeRoutedKey{vk: vk, qcode: qcode})
		out.Events = append(out.Events, nativeQKeyEvent{QCode: qcode, Down: true})
	}
	return out
}
