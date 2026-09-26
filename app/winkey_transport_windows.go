//go:build windows

package main

import (
	"context"
	"errors"
	"sort"
	"sync/atomic"
	"time"
)

// The LL keyboard hook may only make a bounded, nonblocking enqueue. QMP is
// owned by one worker, which waits for an acknowledgement of every ordered
// batch before considering a key released.
type winKeyBatch struct {
	generation uint64
	events     []nativeQKeyEvent
}

type winKeyTransport struct {
	queue      chan winKeyBatch
	generation atomic.Uint64
	ready      atomic.Bool
	pressed    map[string]bool // worker only: acknowledged or ambiguous downs
	order      []string        // down order, for reverse-order recovery releases
}

const winKeyQueueLimit = 64

func newWinKeyTransport() *winKeyTransport {
	t := &winKeyTransport{queue: make(chan winKeyBatch, winKeyQueueLimit), pressed: make(map[string]bool)}
	t.generation.Store(1)
	return t
}

func (t *winKeyTransport) invalidate() {
	t.ready.Store(false)
	t.generation.Add(1)
}

// enqueue returns false if no safe connection/queue exists. Its caller still
// swallows already-consumed physical keys, and resets its local routing state.
func (t *winKeyTransport) enqueue(events []nativeQKeyEvent) bool {
	if len(events) == 0 {
		return true
	}
	if len(events) > 64 || !t.ready.Load() {
		return false
	}
	generation := t.generation.Load()
	batch := winKeyBatch{generation: generation, events: append([]nativeQKeyEvent(nil), events...)}
	select {
	case t.queue <- batch:
		return generation == t.generation.Load()
	default:
		t.invalidate()
		return false
	}
}

type winKeySender interface {
	SendKeys(context.Context, []nativeQKeyEvent) error
}

type qmpWinKeySender struct{ client *qmpClient }

func (s qmpWinKeySender) SendKeys(ctx context.Context, keys []nativeQKeyEvent) error {
	type qmpKey struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	type qmpData struct {
		Down bool   `json:"down"`
		Key  qmpKey `json:"key"`
	}
	type qmpEvent struct {
		Type string  `json:"type"`
		Data qmpData `json:"data"`
	}
	events := make([]qmpEvent, 0, len(keys))
	for _, key := range keys {
		events = append(events, qmpEvent{Type: "key", Data: qmpData{Down: key.Down,
			Key: qmpKey{Type: "qcode", Data: key.QCode}}})
	}
	return s.client.Call(ctx, "input-send-event", struct {
		Events []qmpEvent `json:"events"`
	}{events}, nil)
}

func (t *winKeyTransport) markPossiblyDown(events []nativeQKeyEvent) {
	for _, event := range events {
		if event.Down && !t.pressed[event.QCode] {
			t.pressed[event.QCode] = true
			t.order = append(t.order, event.QCode)
		}
	}
}

func (t *winKeyTransport) acknowledge(events []nativeQKeyEvent) {
	for _, event := range events {
		if event.Down {
			if !t.pressed[event.QCode] {
				t.pressed[event.QCode] = true
				t.order = append(t.order, event.QCode)
			}
		} else {
			delete(t.pressed, event.QCode)
		}
	}
	// Keep the order bounded even after many short chords.
	filtered := t.order[:0]
	for _, code := range t.order {
		if t.pressed[code] {
			filtered = append(filtered, code)
		}
	}
	t.order = filtered
}

func (t *winKeyTransport) releaseEvents() []nativeQKeyEvent {
	events := make([]nativeQKeyEvent, 0, len(t.pressed))
	for i := len(t.order) - 1; i >= 0; i-- {
		if t.pressed[t.order[i]] {
			events = append(events, nativeQKeyEvent{QCode: t.order[i], Down: false})
		}
	}
	// Defensive recovery if a future producer bypasses order bookkeeping.
	if len(events) != len(t.pressed) {
		known := make(map[string]bool, len(events))
		for _, event := range events {
			known[event.QCode] = true
		}
		missing := make([]string, 0)
		for code := range t.pressed {
			if !known[code] {
				missing = append(missing, code)
			}
		}
		sort.Strings(missing)
		for _, code := range missing {
			events = append(events, nativeQKeyEvent{QCode: code})
		}
	}
	return events
}

func (t *winKeyTransport) send(ctx context.Context, sender winKeySender, events []nativeQKeyEvent) error {
	if len(events) == 0 {
		return nil
	}
	t.markPossiblyDown(events) // QMP may apply a down even if its reply is lost.
	callCtx, cancel := context.WithTimeout(ctx, 1200*time.Millisecond)
	defer cancel()
	if err := sender.SendKeys(callCtx, events); err != nil {
		return err
	}
	t.acknowledge(events)
	return nil
}

func (t *winKeyTransport) drain() {
	for {
		select {
		case <-t.queue:
		default:
			return
		}
	}
}

func (t *winKeyTransport) reset(ctx context.Context, sender winKeySender) error {
	t.ready.Store(false)
	t.drain() // Never replay downs from an older connection or overflowed queue.
	if err := t.send(ctx, sender, t.releaseEvents()); err != nil {
		return err
	}
	return nil
}

// runSession returns on QMP error, disconnect, or VM shutdown. The next QMP
// connection releases every possibly pressed key before accepting new input.
func (t *winKeyTransport) runSession(ctx context.Context, sender winKeySender, guestRunning func() bool) error {
	t.invalidate() // The previous socket may have applied an unacknowledged down.
	if err := t.reset(ctx, sender); err != nil {
		return err
	}
	active := t.generation.Load()
	t.ready.Store(true)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if !guestRunning() {
			t.invalidate()
			return errors.New("guest stopped")
		}
		if current := t.generation.Load(); current != active {
			if err := t.reset(ctx, sender); err != nil {
				return err
			}
			active = t.generation.Load()
			t.ready.Store(true)
		}
		select {
		case <-ctx.Done():
			t.invalidate()
			return ctx.Err()
		case batch := <-t.queue:
			if batch.generation != active || t.generation.Load() != active {
				continue
			}
			if err := t.send(ctx, sender, batch.events); err != nil {
				t.invalidate()
				return err
			}
		case <-ticker.C:
		}
	}
}
