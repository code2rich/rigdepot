// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package events

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/code2rich/rigdepot/internal/logging"
	"github.com/code2rich/rigdepot/internal/server/dbx"
)

// Payload is what an event carries. EventName names the kind of event: the publishing domain
// and what happened, lower case, such as "access.membership_changed" or "catalog.published".
// Domain packages that publish after their own commit (access.Publisher) use the same method,
// so their event types are payloads as they are.
type Payload interface {
	EventName() string
}

// Event is one published event.
type Event struct {
	ID      string    // a UUIDv7, unique per event
	Name    string    // Payload.EventName()
	At      time.Time // when it was published, UTC
	Payload Payload
}

// Handler receives events. Its context ends when the bus closes or the handler has taken longer
// than the bus's handler timeout.
type Handler func(ctx context.Context, ev Event) error

// Tx is the transaction an event is published in. *dbx.Tx implements it.
type Tx interface {
	AfterCommit(func())
}

var _ Tx = (*dbx.Tx)(nil)

// DefaultHandlerTimeout is how long one handler call may take.
const DefaultHandlerTimeout = time.Minute

// Bus delivers events to subscribers. The zero value is not usable; call New.
type Bus struct {
	log     *slog.Logger
	timeout time.Duration
	ctx     context.Context
	cancel  context.CancelFunc

	mu       sync.Mutex
	subs     []*subscription
	closed   bool
	inflight int           // events queued or being handled, over all subscribers
	idle     chan struct{} // closed while inflight is 0
}

// New returns a bus that logs failed handlers to log (nil discards).
func New(log *slog.Logger) *Bus {
	if log == nil {
		log = logging.Discard()
	}
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	return &Bus{log: log, timeout: DefaultHandlerTimeout, ctx: ctx, cancel: cancel, idle: idle}
}

// SetHandlerTimeout changes how long one handler call may take. Call it before subscribing.
func (b *Bus) SetHandlerTimeout(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.timeout = d
}

// Publish delivers p to the subscribers once tx has committed. When tx rolls back, nothing is
// delivered.
func (b *Bus) Publish(tx Tx, p Payload) {
	ev := Event{ID: dbx.NewID(), Name: p.EventName(), At: time.Now().UTC(), Payload: p}
	tx.AfterCommit(func() { b.dispatch(ev) })
}

// Publisher adapts a Bus to the publisher interface of a domain package that publishes its
// events itself once its transaction has committed. Instantiated with that package's event
// type, its Publish has exactly the signature the package asks for:
//
//	access.New(db, access.Options{Events: events.Publisher[access.Event]{Bus: bus}, …})
type Publisher[E Payload] struct {
	Bus *Bus
}

// Publish delivers e; the caller has already committed the change.
func (p Publisher[E]) Publish(_ context.Context, e E) { p.Bus.PublishCommitted(e) }

// PublishCommitted delivers p right away. It is for changes that are not made in a transaction
// of this database (a job that finished, a fetch that failed); everything else uses Publish.
func (b *Bus) PublishCommitted(p Payload) {
	b.dispatch(Event{ID: dbx.NewID(), Name: p.EventName(), At: time.Now().UTC(), Payload: p})
}

func (b *Bus) dispatch(ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		b.log.Warn("event published after the bus closed; dropped", "event", ev.Name, "event_id", ev.ID)
		return
	}
	for _, s := range b.subs {
		if s.wants(ev.Name) && s.push(ev) {
			b.addInflight(1)
		}
	}
}

// addInflight changes the number of undelivered events. b.mu must be held.
func (b *Bus) addInflight(n int) {
	before := b.inflight
	b.inflight += n
	switch {
	case before == 0 && b.inflight > 0:
		b.idle = make(chan struct{})
	case before > 0 && b.inflight == 0:
		close(b.idle)
	}
}

func (b *Bus) done(n int) {
	b.mu.Lock()
	b.addInflight(-n)
	b.mu.Unlock()
}

// Subscribe calls h for every event with one of the given names, or for every event when names
// is empty.
// name identifies the subscriber in logs. The returned function ends the subscription: events
// not yet handled are dropped, and a call in progress is left to finish.
func (b *Bus) Subscribe(name string, names []string, h Handler) (unsubscribe func()) {
	s := &subscription{bus: b, name: name, names: slices.Clone(names), h: h, wake: make(chan struct{}, 1), stop: make(chan struct{})}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return func() {}
	}
	b.subs = append(b.subs, s)
	timeout := b.timeout
	b.mu.Unlock()
	go s.run(timeout)
	var once sync.Once
	return func() { once.Do(func() { b.remove(s) }) }
}

func (b *Bus) remove(s *subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs = slices.DeleteFunc(b.subs, func(x *subscription) bool { return x == s })
	dropped := s.close()
	b.addInflight(-dropped)
	if dropped > 0 {
		b.log.Warn("subscription ended with events not handled", "subscriber", s.name, "dropped", dropped)
	}
}

// Drain waits until every event published so far has been handled, or ctx ends.
func (b *Bus) Drain(ctx context.Context) error {
	b.mu.Lock()
	idle := b.idle
	b.mu.Unlock()
	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("events: %d events not handled yet: %w", b.pending(), ctx.Err())
	}
}

func (b *Bus) pending() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inflight
}

// Close stops accepting events, waits for the queued ones to be handled until ctx ends, then
// stops every subscriber; handlers still running see their context canceled.
func (b *Bus) Close(ctx context.Context) error {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	err := b.Drain(ctx)
	b.cancel()
	b.mu.Lock()
	subs := b.subs
	b.subs = nil
	for _, s := range subs {
		b.addInflight(-s.close())
	}
	b.mu.Unlock()
	return err
}

// subscription is one subscriber with its own queue and goroutine.
type subscription struct {
	bus   *Bus
	name  string
	names []string
	h     Handler
	wake  chan struct{}
	stop  chan struct{}

	mu     sync.Mutex
	queue  []Event
	closed bool
}

func (s *subscription) wants(name string) bool {
	return len(s.names) == 0 || slices.Contains(s.names, name)
}

func (s *subscription) push(ev Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.queue = append(s.queue, ev)
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}

// close stops the subscription and returns how many queued events it dropped.
func (s *subscription) close() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0
	}
	s.closed = true
	close(s.stop)
	n := len(s.queue)
	s.queue = nil
	return n
}

func (s *subscription) pop() (Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || len(s.queue) == 0 {
		return Event{}, false
	}
	ev := s.queue[0]
	s.queue[0] = Event{}
	s.queue = s.queue[1:]
	return ev, true
}

func (s *subscription) run(timeout time.Duration) {
	for {
		select {
		case <-s.stop:
			return
		case <-s.wake:
		}
		for {
			ev, ok := s.pop()
			if !ok {
				break
			}
			s.handle(ev, timeout)
			s.bus.done(1)
		}
	}
}

// handle calls the handler for one event. An error or a panic is logged and ends there: it
// never reaches whoever published the event.
func (s *subscription) handle(ev Event, timeout time.Duration) {
	ctx, cancel := context.WithTimeout(s.bus.ctx, timeout)
	defer cancel()
	err := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("panic: %v\n%s", p, debug.Stack())
			}
		}()
		return s.h(ctx, ev)
	}()
	if err != nil {
		level := slog.LevelError
		if errors.Is(err, context.Canceled) && s.bus.ctx.Err() != nil {
			level = slog.LevelWarn
		}
		s.bus.log.Log(ctx, level, "event handler failed", "subscriber", s.name, "event", ev.Name, "event_id", ev.ID, "err", err)
	}
}
