// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package events_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code2rich/rigdepot/internal/server/dbx"
	"github.com/code2rich/rigdepot/internal/server/dbx/dbxtest"
	"github.com/code2rich/rigdepot/internal/server/events"
	"github.com/code2rich/rigdepot/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

type thingDone struct{ Name string }

func (thingDone) EventKind() string { return "thing.done" }

type otherHappened struct{}

func (otherHappened) EventKind() string { return "other.happened" }

// recorder collects the events a subscriber got.
type recorder struct {
	mu  sync.Mutex
	got []string
}

func (r *recorder) handler(ctx context.Context, ev events.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := ev.Kind
	if p, ok := ev.Payload.(thingDone); ok {
		name += ":" + p.Name
	}
	r.got = append(r.got, name)
	return nil
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.got)
}

func drain(t *testing.T, b *events.Bus) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestDeliveredOnlyAfterCommit: an event published in a transaction reaches subscribers once the
// transaction commits, and never when it rolls back.
func TestDeliveredOnlyAfterCommit(t *testing.T) {
	dbxtest.Each(t, func(t *testing.T, db *dbx.DB) {
		ctx := context.Background()
		bus := events.New(nil)
		defer bus.Close(ctx)
		var r recorder
		bus.Subscribe("test", nil, r.handler)

		var beforeCommit []string
		_ = db.InTx(ctx, func(tx *dbx.Tx) error {
			bus.Publish(tx, thingDone{"rolled-back"})
			return errors.New("boom")
		})
		err := db.InTx(ctx, func(tx *dbx.Tx) error {
			bus.Publish(tx, thingDone{"committed"})
			// Give a wrongly immediate delivery the chance to show.
			time.Sleep(20 * time.Millisecond)
			beforeCommit = r.list()
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, bus)
		if len(beforeCommit) != 0 {
			t.Errorf("delivered before the commit: %v", beforeCommit)
		}
		if got := r.list(); !slices.Equal(got, []string{"thing.done:committed"}) {
			t.Errorf("got %v, want only the committed event", got)
		}
	})
}

func TestKindsOrderAndUnsubscribe(t *testing.T) {
	bus := events.New(nil)
	defer bus.Close(context.Background())
	var all, things recorder
	bus.Subscribe("all", nil, all.handler)
	stop := bus.Subscribe("things", []string{"thing.done"}, things.handler)

	for i := range 50 {
		bus.PublishCommitted(thingDone{string(rune('a' + i%26))})
		bus.PublishCommitted(otherHappened{})
	}
	drain(t, bus)
	if n := len(all.list()); n != 100 {
		t.Errorf("the subscriber to every kind got %d events, want 100", n)
	}
	got := things.list()
	if len(got) != 50 {
		t.Fatalf("the subscriber to thing.done got %d events, want 50: %v", len(got), got)
	}
	for i, g := range got {
		if want := "thing.done:" + string(rune('a'+i%26)); g != want {
			t.Fatalf("event %d is %s, want %s: delivered out of order", i, g, want)
		}
	}

	stop()
	stop() // twice is harmless
	bus.PublishCommitted(thingDone{"late"})
	drain(t, bus)
	if n := len(things.list()); n != 50 {
		t.Errorf("an ended subscription still got events: %d", n)
	}
}

// TestFailingSubscriberIsIsolated: a handler that fails or panics is logged; the publisher does
// not notice, and the other subscribers still get the event.
func TestFailingSubscriberIsIsolated(t *testing.T) {
	var logs syncBuffer
	bus := events.New(slog.New(slog.NewTextHandler(&logs, nil)))
	defer bus.Close(context.Background())
	bus.Subscribe("fails", nil, func(context.Context, events.Event) error { return errors.New("cannot") })
	bus.Subscribe("panics", nil, func(context.Context, events.Event) error { panic("boom") })
	var ok recorder
	bus.Subscribe("ok", nil, ok.handler)

	bus.PublishCommitted(thingDone{"x"})
	drain(t, bus)
	if got := ok.list(); !slices.Equal(got, []string{"thing.done:x"}) {
		t.Errorf("the healthy subscriber got %v", got)
	}
	out := logs.String()
	for _, want := range []string{"subscriber=fails", "cannot", "subscriber=panics", "boom"} {
		if !strings.Contains(out, want) {
			t.Errorf("the log does not mention %q:\n%s", want, out)
		}
	}
}

// TestSlowSubscriberDoesNotBlockPublisher: publishing returns at once even when a subscriber is
// stuck, and the handler's context ends after the handler timeout.
func TestSlowSubscriberDoesNotBlockPublisher(t *testing.T) {
	bus := events.New(nil)
	bus.SetHandlerTimeout(50 * time.Millisecond)
	release := make(chan struct{})
	ended := make(chan error, 1)
	var first sync.Once
	bus.Subscribe("slow", nil, func(ctx context.Context, _ events.Event) error {
		first.Do(func() {
			<-ctx.Done()
			ended <- ctx.Err()
			<-release
		})
		return nil
	})
	start := time.Now()
	for range 100 {
		bus.PublishCommitted(otherHappened{})
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("publishing took %v with a stuck subscriber", d)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("the handler's context ended with %v, want the deadline", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the handler's context did not end after the timeout")
	}
	close(release)
	drain(t, bus)
	if err := bus.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseDrainsThenDropsLateEvents(t *testing.T) {
	bus := events.New(nil)
	var r recorder
	bus.Subscribe("r", nil, r.handler)
	for range 10 {
		bus.PublishCommitted(otherHappened{})
	}
	if err := bus.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := len(r.list()); n != 10 {
		t.Errorf("Close did not wait for queued events: %d of 10 handled", n)
	}
	bus.PublishCommitted(otherHappened{})
	bus.Subscribe("late", nil, r.handler)()
	if n := len(r.list()); n != 10 {
		t.Errorf("an event published after Close was delivered")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
