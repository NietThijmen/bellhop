// Package events is the daemon's in-process event bus. Every state change and
// log line that clients may want to see is published here and fanned out to
// subscribers (the SSE endpoint, the job runner, tests).
//
// It lives outside internal/daemon so that lower layers (supervisor, jobs,
// proxy, ...) can publish without importing the daemon.
package events

import (
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
)

// Publisher is the narrow interface lower layers depend on.
type Publisher interface {
	Publish(typ string, data any)
}

// PublisherFunc adapts a function to Publisher.
type PublisherFunc func(typ string, data any)

// Publish implements Publisher.
func (f PublisherFunc) Publish(typ string, data any) { f(typ, data) }

// Discard is a Publisher that drops everything; handy in tests.
var Discard Publisher = PublisherFunc(func(string, any) {})

type subscriber struct {
	ch     chan api.Event
	closed bool
}

// Bus fans events out to subscribers and keeps a short history so a client
// reconnecting with Last-Event-ID misses nothing that is still buffered.
//
// A subscriber that cannot keep up (its buffer is full) is disconnected: its
// channel is closed and it is expected to resubscribe with the last ID it saw.
// Publishing therefore never blocks.
type Bus struct {
	mu      sync.Mutex
	nextID  uint64
	history []api.Event // ring, oldest first once full
	histCap int
	subs    map[*subscriber]struct{}
	now     func() time.Time
}

// NewBus returns a bus that retains the last historySize events for replay.
func NewBus(historySize int) *Bus {
	if historySize < 0 {
		historySize = 0
	}
	return &Bus{histCap: historySize, subs: map[*subscriber]struct{}{}, now: time.Now}
}

// Publish marshals data and delivers the event to every subscriber.
// Marshal failures are logged and the event is dropped.
func (b *Bus) Publish(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Error("events: cannot marshal event", "type", typ, "err", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	ev := api.Event{ID: b.nextID, Type: typ, Time: b.now().UTC(), Data: raw}
	if b.histCap > 0 {
		if len(b.history) == b.histCap {
			copy(b.history, b.history[1:])
			b.history = b.history[:b.histCap-1]
		}
		b.history = append(b.history, ev)
	}
	for s := range b.subs {
		select {
		case s.ch <- ev:
		default:
			b.dropLocked(s)
		}
	}
}

// Subscribe returns a channel of events with IDs greater than sinceID
// (0 = only new events) and a cancel function that must be called when the
// subscriber is done. The channel is closed on cancel or when the subscriber
// falls behind by more than buffer events.
func (b *Bus) Subscribe(buffer int, sinceID uint64) (<-chan api.Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var replay []api.Event
	if sinceID > 0 {
		for _, ev := range b.history {
			if ev.ID > sinceID {
				replay = append(replay, ev)
			}
		}
	}
	size := buffer
	if len(replay) > size {
		size = len(replay) + buffer
	}
	s := &subscriber{ch: make(chan api.Event, size)}
	for _, ev := range replay {
		s.ch <- ev
	}
	b.subs[s] = struct{}{}
	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.dropLocked(s)
		})
	}
}

// LastID returns the ID of the most recently published event.
func (b *Bus) LastID() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextID
}

func (b *Bus) dropLocked(s *subscriber) {
	if s.closed {
		return
	}
	s.closed = true
	delete(b.subs, s)
	close(s.ch)
}
