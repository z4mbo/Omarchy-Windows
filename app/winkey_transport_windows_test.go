//go:build windows

package main

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type fakeWinKeySender struct {
	calls  [][]nativeQKeyEvent
	failAt int
}

func (f *fakeWinKeySender) SendKeys(_ context.Context, events []nativeQKeyEvent) error {
	f.calls = append(f.calls, append([]nativeQKeyEvent(nil), events...))
	if f.failAt == len(f.calls) {
		return errors.New("lost QMP acknowledgement")
	}
	return nil
}

func TestWinKeyTransportOrderedBatchAndReverseRelease(t *testing.T) {
	transport := newWinKeyTransport()
	sender := &fakeWinKeySender{}
	batch := []nativeQKeyEvent{{"meta_l", true}, {"shift", true}, {"1", true}, {"1", false}}
	if err := transport.send(context.Background(), sender, batch); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sender.calls, [][]nativeQKeyEvent{batch}) {
		t.Fatalf("QMP batches = %+v", sender.calls)
	}
	wantRelease := []nativeQKeyEvent{{"shift", false}, {"meta_l", false}}
	if got := transport.releaseEvents(); !reflect.DeepEqual(got, wantRelease) {
		t.Fatalf("recovery release = %+v, want %+v", got, wantRelease)
	}
	if err := transport.reset(context.Background(), sender); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sender.calls[1], wantRelease) || len(transport.pressed) != 0 {
		t.Fatalf("reset did not acknowledge all releases: calls=%+v pressed=%+v", sender.calls, transport.pressed)
	}
}

func TestWinKeyTransportAmbiguousDownReleasesBeforeNewInput(t *testing.T) {
	transport := newWinKeyTransport()
	failed := &fakeWinKeySender{failAt: 1}
	if err := transport.send(context.Background(), failed, []nativeQKeyEvent{{"meta_l", true}}); err == nil {
		t.Fatal("missing QMP acknowledgement accepted")
	}
	// An old queued chord must never be replayed on the replacement socket.
	transport.queue <- winKeyBatch{generation: 1, events: []nativeQKeyEvent{{"2", true}}}
	reconnected := &fakeWinKeySender{}
	if err := transport.reset(context.Background(), reconnected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reconnected.calls, [][]nativeQKeyEvent{{{"meta_l", false}}}) {
		t.Fatalf("reconnection did not release the ambiguous key first: %+v", reconnected.calls)
	}
	if len(transport.queue) != 0 || len(transport.pressed) != 0 {
		t.Fatalf("stale queue or guest key retained: queue=%d pressed=%+v", len(transport.queue), transport.pressed)
	}
}

func TestWinKeyTransportOverflowInvalidatesEntireGeneration(t *testing.T) {
	transport := newWinKeyTransport()
	sender := &fakeWinKeySender{}
	if err := transport.send(context.Background(), sender, []nativeQKeyEvent{{"meta_l", true}}); err != nil {
		t.Fatal(err)
	}
	transport.ready.Store(true)
	for i := 0; i < winKeyQueueLimit; i++ {
		if !transport.enqueue([]nativeQKeyEvent{{"a", true}}) {
			t.Fatalf("early overflow at %d", i)
		}
	}
	old := transport.generation.Load()
	if transport.enqueue([]nativeQKeyEvent{{"b", true}}) || transport.ready.Load() || transport.generation.Load() <= old {
		t.Fatal("overflow did not invalidate pending downs")
	}
	if err := transport.reset(context.Background(), sender); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sender.calls[1:], [][]nativeQKeyEvent{{{"meta_l", false}}}) || len(transport.queue) != 0 {
		t.Fatalf("stale downs replayed or meta stuck: calls=%+v queue=%d", sender.calls, len(transport.queue))
	}
}

func TestWinKeyTransportFailedRecoveryKeepsKeysAmbiguous(t *testing.T) {
	transport := newWinKeyTransport()
	first := &fakeWinKeySender{}
	if err := transport.send(context.Background(), first, []nativeQKeyEvent{{"meta_l", true}}); err != nil {
		t.Fatal(err)
	}
	failedRecovery := &fakeWinKeySender{failAt: 1}
	if err := transport.reset(context.Background(), failedRecovery); err == nil {
		t.Fatal("failed release accepted")
	}
	if !transport.pressed["meta_l"] {
		t.Fatal("ambiguous meta key forgotten")
	}
	good := &fakeWinKeySender{}
	if err := transport.reset(context.Background(), good); err != nil {
		t.Fatal(err)
	}
	if len(good.calls) != 1 || !reflect.DeepEqual(good.calls[0], []nativeQKeyEvent{{"meta_l", false}}) {
		t.Fatalf("next connection failed to release: %+v", good.calls)
	}
}

type gatedWinKeySender struct {
	calls     chan []nativeQKeyEvent
	firstGate chan struct{}
	count     atomic.Uint32
}

func (s *gatedWinKeySender) SendKeys(ctx context.Context, events []nativeQKeyEvent) error {
	select {
	case s.calls <- append([]nativeQKeyEvent(nil), events...):
	case <-ctx.Done():
		return ctx.Err()
	}
	if s.count.Add(1) == 1 {
		select {
		case <-s.firstGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestWinKeyTransportOverflowDuringUnacknowledgedDownReleasesBeforeResume(t *testing.T) {
	transport := newWinKeyTransport()
	sender := &gatedWinKeySender{calls: make(chan []nativeQKeyEvent, 4), firstGate: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- transport.runSession(ctx, sender, func() bool { return true }) }()
	deadline := time.After(time.Second)
	for !transport.ready.Load() {
		select {
		case <-deadline:
			t.Fatal("key transport never became ready")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !transport.enqueue([]nativeQKeyEvent{{"meta_l", true}}) {
		t.Fatal("first down not queued")
	}
	select {
	case got := <-sender.calls:
		if !reflect.DeepEqual(got, []nativeQKeyEvent{{"meta_l", true}}) {
			t.Fatalf("first QMP batch = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("first QMP send did not start")
	}
	for i := 0; i < winKeyQueueLimit; i++ {
		if !transport.enqueue([]nativeQKeyEvent{{"a", true}}) {
			t.Fatalf("early overflow at %d", i)
		}
	}
	if transport.enqueue([]nativeQKeyEvent{{"b", true}}) {
		t.Fatal("overflowed generation accepted a down")
	}
	close(sender.firstGate)
	select {
	case got := <-sender.calls:
		if !reflect.DeepEqual(got, []nativeQKeyEvent{{"meta_l", false}}) {
			t.Fatalf("stale down preceded recovery release: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery release was not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("transport did not stop")
	}
}
