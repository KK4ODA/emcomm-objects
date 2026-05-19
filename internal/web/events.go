package web

import (
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/kiss"
	"github.com/kk4oda/emcomm-objects/internal/transmit"
)

// EventType discriminates the kind of event sent over SSE.
type EventType string

const (
	EventPacket EventType = "packet"
	EventState  EventType = "state"
	EventLog    EventType = "log"
)

// Event is what each SSE subscriber receives.
type Event struct {
	Type EventType `json:"type"`
	When time.Time `json:"when"`

	// Set for type=packet:
	Packet *PacketSummary `json:"packet,omitempty"`

	// Set for type=state:
	KISSState string `json:"kiss_state,omitempty"`

	// Set for type=log:
	Level   string `json:"level,omitempty"`
	Message string `json:"message,omitempty"`
}

// PacketSummary is the public shape of a transmitted packet for the UI.
type PacketSummary struct {
	Object   string `json:"object"`
	Killed   bool   `json:"killed"` // true = kill packet ('_' indicator), false = live ('*')
	Source   string `json:"source"`
	Dest     string `json:"dest"`
	Path     string `json:"path"`
	Info     string `json:"info"`
	NumBytes int    `json:"num_bytes"`
}

// Broker fans out events to all active SSE subscribers.
type Broker struct {
	mu         sync.RWMutex
	subs       map[chan Event]struct{}
	recent     []Event // bounded ring of recent events for new subscribers
	maxRecent  int
	recentHead int // next write index when wrapped
}

// NewBroker creates a broker that keeps the last `maxRecent` events for
// replay to new subscribers.
func NewBroker(maxRecent int) *Broker {
	if maxRecent < 0 {
		maxRecent = 0
	}
	return &Broker{
		subs:      make(map[chan Event]struct{}),
		recent:    make([]Event, 0, maxRecent),
		maxRecent: maxRecent,
	}
}

// Subscribe returns a channel that receives all future events plus the
// recent backlog. Caller must Unsubscribe when done.
func (b *Broker) Subscribe() chan Event {
	ch := make(chan Event, 32)
	b.mu.Lock()
	// Replay recent events in chronological order.
	for _, e := range b.snapshotLocked() {
		select {
		case ch <- e:
		default:
			// Subscriber buffer too small; drop replay.
		}
	}
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber.
func (b *Broker) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// Publish sends an event to all subscribers (non-blocking) and stores it
// in the recent ring.
func (b *Broker) Publish(e Event) {
	if e.When.IsZero() {
		e.When = time.Now().UTC()
	}
	b.mu.Lock()
	b.appendRecentLocked(e)
	subs := make([]chan Event, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
			// Slow subscriber — drop this event for them.
		}
	}
}

func (b *Broker) appendRecentLocked(e Event) {
	if b.maxRecent == 0 {
		return
	}
	if len(b.recent) < b.maxRecent {
		b.recent = append(b.recent, e)
		return
	}
	b.recent[b.recentHead] = e
	b.recentHead = (b.recentHead + 1) % b.maxRecent
}

func (b *Broker) snapshotLocked() []Event {
	if b.maxRecent == 0 || len(b.recent) == 0 {
		return nil
	}
	if len(b.recent) < b.maxRecent {
		// Not yet wrapped.
		out := make([]Event, len(b.recent))
		copy(out, b.recent)
		return out
	}
	out := make([]Event, b.maxRecent)
	for i := 0; i < b.maxRecent; i++ {
		out[i] = b.recent[(b.recentHead+i)%b.maxRecent]
	}
	return out
}

// RecentEvents returns a snapshot of the most recent events, oldest first.
func (b *Broker) RecentEvents() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.snapshotLocked()
}

// PacketHook returns a function suitable as transmit.Sender.OnPacket.
func (b *Broker) PacketHook() func(transmit.PacketEvent) {
	return func(pe transmit.PacketEvent) {
		pathStr := ""
		for i, p := range pe.Path {
			if i > 0 {
				pathStr += ","
			}
			pathStr += p.String()
		}
		b.Publish(Event{
			Type: EventPacket,
			When: pe.When,
			Packet: &PacketSummary{
				Object:   pe.Object,
				Killed:   pe.Killed,
				Source:   pe.Source.String(),
				Dest:     pe.Dest.String(),
				Path:     pathStr,
				Info:     pe.Info,
				NumBytes: pe.NumBytes,
			},
		})
	}
}

// KISSStateHook returns a function suitable as kiss.Client onState callback.
func (b *Broker) KISSStateHook() func(kiss.State) {
	return func(s kiss.State) {
		b.Publish(Event{Type: EventState, KISSState: s.String()})
	}
}
