// Package events is the server's event bus (SPEC-502, SPEC-503). Every event
// gets an id "<run>-<seq>"; the most recent events are kept in a ring buffer
// so that a reconnecting client can catch up. A client whose id is unknown
// (another run, or dropped out of the buffer) gets a resync event instead.
package events

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Event is one published event. Data is compact JSON (no newlines).
type Event struct {
	ID   string
	Type string
	Data json.RawMessage
}

// SubBuffer is the channel capacity of a subscriber. A subscriber that
// falls further behind is dropped with Lagged set (SPEC-502).
const SubBuffer = 256

// Bus fans events out to subscribers and keeps a replay buffer. The zero
// value is not usable; call New.
type Bus struct {
	mu     sync.Mutex
	run    string
	seq    uint64
	ring   []Event // ring[seq % len] holds the event with that seq
	subs   map[*Sub]struct{}
	closed bool
}

// New returns a bus for the given run id with a replay buffer of size events.
func New(run string, size int) *Bus {
	if size < 1 {
		size = 1
	}
	return &Bus{run: run, ring: make([]Event, size), subs: map[*Sub]struct{}{}}
}

// Run returns the run id.
func (b *Bus) Run() string { return b.run }

// Publish assigns the next id and delivers the event. It never blocks: a
// subscriber whose channel is full is dropped and marked Lagged. Publishing
// on a closed bus is a no-op.
func (b *Bus) Publish(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		// Only our own apitypes values are published; this is a programming error.
		panic(fmt.Sprintf("events: marshal %s: %v", typ, err))
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.seq++
	ev := Event{ID: b.id(b.seq), Type: typ, Data: raw}
	b.ring[b.seq%uint64(len(b.ring))] = ev
	for s := range b.subs {
		select {
		case s.c <- ev:
		default:
			s.lagged = true
			b.drop(s)
		}
	}
}

func (b *Bus) id(seq uint64) string { return b.run + "-" + strconv.FormatUint(seq, 10) }

// LastID returns the id of the newest event, or "<run>-0" if there is none.
func (b *Bus) LastID() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.id(b.seq)
}

// Subscribe registers a subscriber. lastID is the client's Last-Event-ID
// ("" for a fresh client). The returned replay holds the events the client
// missed; if those can't be replayed, replay is a single resync event that
// carries the newest id. Registration and replay are atomic, so no event is
// lost or duplicated. ok is false when the bus is closed.
func (b *Bus) Subscribe(lastID string) (s *Sub, replay []Event, ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, nil, false
	}
	s = &Sub{bus: b, c: make(chan Event, SubBuffer)}
	b.subs[s] = struct{}{}
	if lastID == "" {
		return s, nil, true
	}
	seq, reason := b.parse(lastID)
	if reason == "" {
		oldest := uint64(1)
		if b.seq > uint64(len(b.ring)) {
			oldest = b.seq - uint64(len(b.ring)) + 1
		}
		if seq+1 < oldest {
			reason = "missed events are no longer in the buffer"
		} else {
			for q := seq + 1; q <= b.seq; q++ {
				replay = append(replay, b.ring[q%uint64(len(b.ring))])
			}
			return s, replay, true
		}
	}
	return s, []Event{b.resync(reason)}, true
}

// parse checks a Last-Event-ID against this run; reason is "" when valid.
func (b *Bus) parse(id string) (seq uint64, reason string) {
	run, num, found := strings.Cut(id, "-")
	if !found || run != b.run {
		return 0, "event id is from another server run"
	}
	seq, err := strconv.ParseUint(num, 10, 64)
	if err != nil || seq > b.seq {
		return 0, "unknown event id"
	}
	return seq, ""
}

// Resync returns a resync event carrying the newest id (SPEC-503).
func (b *Bus) Resync(reason string) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.resync(reason)
}

func (b *Bus) resync(reason string) Event {
	raw, _ := json.Marshal(apitypes.ResyncEvent{Reason: reason})
	return Event{ID: b.id(b.seq), Type: apitypes.EventResync, Data: raw}
}

// Close ends every subscription and makes later Publish and Subscribe calls
// no-ops (SPEC-804).
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for s := range b.subs {
		b.drop(s)
	}
}

func (b *Bus) drop(s *Sub) {
	delete(b.subs, s)
	close(s.c)
}

// Sub is one subscription. Read events from C until it is closed; then
// Lagged tells whether the subscriber was dropped for falling behind.
type Sub struct {
	bus    *Bus
	c      chan Event
	lagged bool
}

// C returns the event channel. It is closed when the subscription ends.
func (s *Sub) C() <-chan Event { return s.c }

// Lagged reports whether the subscription was dropped because the
// subscriber fell behind. Only meaningful after C is closed.
func (s *Sub) Lagged() bool {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	return s.lagged
}

// Close ends the subscription. It is safe to call more than once.
func (s *Sub) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	if _, ok := s.bus.subs[s]; ok {
		s.bus.drop(s)
	}
}
