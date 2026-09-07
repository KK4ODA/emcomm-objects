// Package transmit turns a stored object into an APRS packet and hands it
// to the active transport (Graywolf REST or KISS). It also owns the
// Router, which lets Settings switch transports at runtime.
package transmit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/aprs"
	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

// ErrNotConfigured is returned while the station callsign is missing; the
// scheduler treats it like a down transport (retry quietly).
var ErrNotConfigured = errors.New("station not configured")

// Ready reports whether scheduled beacons can go out: callsign set and
// transport connected. Suitable as scheduler.Options.Ready.
func (s *Sender) Ready() (bool, string) {
	if err := s.Config().Validate(); err != nil {
		return false, "station not configured: " + err.Error()
	}
	st := s.link.State()
	if st.Status != link.Connected {
		return false, "transport " + st.Transport + " " + st.StatusStr + ": " + st.Detail
	}
	return true, ""
}

// Switchable is a link that can be idled and woken (both transports).
type Switchable interface {
	link.Link
	SetEnabled(bool)
}

// Router forwards to whichever transport the config names. Both links keep
// running; the inactive one is disabled (no dialing, no polling).
type Router struct {
	mu     sync.Mutex
	links  map[string]Switchable
	active string
}

// NewRouter registers links by name (link.Name()).
func NewRouter(links ...Switchable) *Router {
	r := &Router{links: map[string]Switchable{}}
	for _, l := range links {
		r.links[l.Name()] = l
	}
	return r
}

// Use activates the named transport and idles the others.
func (r *Router) Use(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.links[name]; !ok {
		return fmt.Errorf("unknown transport %q", name)
	}
	r.active = name
	for n, l := range r.links {
		l.SetEnabled(n == name)
	}
	return nil
}

// Active returns the active link (nil before Use).
func (r *Router) Active() link.Link {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.links[r.active]; ok {
		return l
	}
	return nil
}

// Name implements link.Link.
func (r *Router) Name() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Send implements link.Link.
func (r *Router) Send(ctx context.Context, p link.Packet) error {
	l := r.Active()
	if l == nil {
		return link.ErrNotConnected
	}
	return l.Send(ctx, p)
}

// Retire implements link.Link.
func (r *Router) Retire(ctx context.Context, object string) error {
	l := r.Active()
	if l == nil {
		return nil
	}
	return l.Retire(ctx, object)
}

// State implements link.Link.
func (r *Router) State() link.State {
	l := r.Active()
	if l == nil {
		return link.State{Transport: "none", Detail: "no transport"}.WithStatus(link.Disconnected)
	}
	return l.State()
}

// Sender builds packets from objects and sends them over a link.
type Sender struct {
	cfg  atomic.Pointer[config.Config]
	link link.Link
	log  *slog.Logger

	// OnPacket observes every successful transmission (UI log, tests).
	OnPacket func(PacketEvent)
}

// PacketEvent captures one transmitted beacon.
type PacketEvent struct {
	When      time.Time
	Object    string
	Killed    bool
	Transport string
	Source    ax25.Address
	Dest      ax25.Address
	Path      []ax25.Address
	Info      string
}

// NewSender wires a Sender. The config can be swapped with SetConfig.
func NewSender(cfg config.Config, l link.Link, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}
	s := &Sender{link: l, log: log}
	s.cfg.Store(&cfg)
	return s
}

// SetConfig applies new station settings for subsequent packets.
func (s *Sender) SetConfig(cfg config.Config) { s.cfg.Store(&cfg) }

// Config returns the current config snapshot.
func (s *Sender) Config() config.Config { return *s.cfg.Load() }

// Transmit sends a LIVE beacon. Suitable as scheduler.TransmitFunc.
func (s *Sender) Transmit(o store.Object, now time.Time) error { return s.send(o, now, false) }

// TransmitKilled sends a KILLED beacon ('_' indicator).
func (s *Sender) TransmitKilled(o store.Object, now time.Time) error { return s.send(o, now, true) }

// Retire tells the transport the object is gone for good.
func (s *Sender) Retire(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return s.link.Retire(ctx, name)
}

// Build returns the packet that would be sent, without sending it.
func (s *Sender) Build(o store.Object, now time.Time, killed bool) (link.Packet, error) {
	cfg := *s.cfg.Load()
	if err := cfg.Validate(); err != nil {
		return link.Packet{}, fmt.Errorf("%w: %v", ErrNotConfigured, err)
	}
	path := cfg.Path()
	switch o.Path {
	case "":
		// station default
	case "-":
		path = nil
	default:
		p, err := ax25.ParsePath(o.Path)
		if err != nil {
			return link.Packet{}, fmt.Errorf("object %q path: %w", o.ObjectName, err)
		}
		path = p
	}
	apr := aprs.Object{
		Name:        o.ObjectName,
		Latitude:    o.Latitude,
		Longitude:   o.Longitude,
		SymbolTable: o.SymbolTableByte(),
		SymbolCode:  o.SymbolCodeByte(),
		Comment:     o.Comment,
		AltitudeFt:  o.Altitude,
	}
	var info string
	var err error
	if killed {
		info, err = aprs.BuildKilledPacket(apr, now)
	} else {
		info, err = aprs.BuildPacket(apr, now)
	}
	if err != nil {
		return link.Packet{}, fmt.Errorf("object %q: build packet: %w", o.ObjectName, err)
	}
	return link.Packet{
		Object:          o.ObjectName,
		Killed:          killed,
		Source:          cfg.Source(),
		Dest:            cfg.Dest(),
		Path:            path,
		Info:            info,
		Latitude:        o.Latitude,
		Longitude:       o.Longitude,
		SymbolTab:       o.SymbolTableByte(),
		SymbolCode:      o.SymbolCodeByte(),
		Comment:         o.Comment,
		AltitudeFt:      o.Altitude,
		IntervalSeconds: o.IntervalMinutes * 60,
	}, nil
}

func (s *Sender) send(o store.Object, now time.Time, killed bool) error {
	p, err := s.Build(o, now, killed)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := s.link.Send(ctx, p); err != nil {
		return fmt.Errorf("object %q: %w", o.ObjectName, err)
	}
	kind := "live"
	if killed {
		kind = "killed"
	}
	s.log.Info("transmitted", "object", o.ObjectName, "kind", kind, "via", s.link.Name(),
		"src", p.Source.String(), "path", p.PathString(), "info", p.Info)
	if s.OnPacket != nil {
		s.OnPacket(PacketEvent{
			When: now, Object: o.ObjectName, Killed: killed, Transport: s.link.Name(),
			Source: p.Source, Dest: p.Dest, Path: path0(p.Path), Info: p.Info,
		})
	}
	return nil
}

func path0(p []ax25.Address) []ax25.Address {
	if len(p) == 0 {
		return nil
	}
	return p
}
