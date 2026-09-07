package web

import (
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/logbuf"
	"github.com/kk4oda/emcomm-objects/internal/transmit"
)

// EventType discriminates the kind of event sent over SSE.
type EventType string

const (
	EventPacket  EventType = "packet"  // a beacon went out
	EventState   EventType = "state"   // transport connection state changed
	EventLog     EventType = "log"     // a server log line
	EventObjects EventType = "objects" // the object list changed; reload it
	EventConfig  EventType = "config"  // settings changed
	EventUpdate  EventType = "update"  // update state changed
)

// Event is what each SSE subscriber receives.
type Event struct {
	Type EventType `json:"type"`
	When time.Time `json:"when"`

	Packet *PacketSummary `json:"packet,omitempty"` // type=packet
	State  *link.State    `json:"state,omitempty"`  // type=state
	Log    *logbuf.Entry  `json:"log,omitempty"`    // type=log
}

// PacketSummary is the public shape of a transmitted packet.
type PacketSummary struct {
	Object    string `json:"object"`
	Killed    bool   `json:"killed"`
	Transport string `json:"transport"`
	Source    string `json:"source"`
	Dest      string `json:"dest"`
	Path      string `json:"path"`
	Info      string `json:"info"`
}

// Broker fans out events to all active SSE subscribers and keeps a bounded
// ring of recent packet/state events for the Activity tab's history.
type Broker struct {
	mu        sync.RWMutex
	subs      map[chan Event]struct{}
	recent    []Event
	maxRecent int
	head      int
	full      bool
}

// NewBroker creates a broker keeping the last maxRecent packet/state events.
func NewBroker(maxRecent int) *Broker {
	if maxRecent < 1 {
		maxRecent = 1
	}
	return &Broker{subs: make(map[chan Event]struct{}), recent: make([]Event, maxRecent), maxRecent: maxRecent}
}

// Subscribe returns a channel that receives all future events. The caller
// must Unsubscribe when done.
func (b *Broker) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
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

// Publish sends an event to all subscribers (non-blocking; a slow browser
// drops events rather than stalling the server) and remembers packet and
// state events.
func (b *Broker) Publish(e Event) {
	if e.When.IsZero() {
		e.When = time.Now().UTC()
	}
	b.mu.Lock()
	if e.Type == EventPacket || e.Type == EventState {
		b.recent[b.head] = e
		b.head = (b.head + 1) % b.maxRecent
		if b.head == 0 {
			b.full = true
		}
	}
	subs := make([]chan Event, 0, len(b.subs))
	for ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// RecentEvents returns remembered events, oldest first.
func (b *Broker) RecentEvents() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.full {
		out := make([]Event, b.head)
		copy(out, b.recent[:b.head])
		return out
	}
	out := make([]Event, b.maxRecent)
	for i := range out {
		out[i] = b.recent[(b.head+i)%b.maxRecent]
	}
	return out
}

// PacketHook returns a transmit.Sender.OnPacket callback.
func (b *Broker) PacketHook() func(transmit.PacketEvent) {
	return func(pe transmit.PacketEvent) {
		path := ""
		for i, p := range pe.Path {
			if i > 0 {
				path += ","
			}
			path += p.String()
		}
		b.Publish(Event{Type: EventPacket, When: pe.When, Packet: &PacketSummary{
			Object: pe.Object, Killed: pe.Killed, Transport: pe.Transport,
			Source: pe.Source.String(), Dest: pe.Dest.String(), Path: path, Info: pe.Info,
		}})
	}
}

// StateHook returns a transport onState callback.
func (b *Broker) StateHook() func(link.State) {
	return func(s link.State) {
		st := s
		b.Publish(Event{Type: EventState, State: &st})
	}
}

// LogHook returns a logbuf notify callback.
func (b *Broker) LogHook() func(logbuf.Entry) {
	return func(e logbuf.Entry) {
		entry := e
		b.Publish(Event{Type: EventLog, When: e.When, Log: &entry})
	}
}

// Notify publishes a bare event of the given type.
func (b *Broker) Notify(t EventType) { b.Publish(Event{Type: t}) }
