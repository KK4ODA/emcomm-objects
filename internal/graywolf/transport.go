package graywolf

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/link"
)

const beaconInterval = 86400 // informational: we schedule, Graywolf does not

// Options for the transport.
type Options struct {
	Channel  int    // Graywolf channel id; 0 = default
	SendPath string // rf | both | is_only
}

// Transport implements link.Link over Graywolf's beacon API.
type Transport struct {
	client *Client
	log    *slog.Logger

	mu      sync.Mutex
	opts    Options
	enabled bool
	state   link.State
	ids     map[string]int // object name -> Graywolf beacon id
	onState func(link.State)
	wake    chan struct{}
}

// NewTransport wires a transport. onState may be nil.
func NewTransport(client *Client, opts Options, log *slog.Logger, onState func(link.State)) *Transport {
	if log == nil {
		log = slog.Default()
	}
	if onState == nil {
		onState = func(link.State) {}
	}
	return &Transport{
		client:  client,
		log:     log,
		opts:    opts,
		ids:     map[string]int{},
		onState: onState,
		state:   link.State{Transport: "graywolf"}.WithStatus(link.Disconnected),
		wake:    make(chan struct{}, 1),
	}
}

// Name implements link.Link.
func (t *Transport) Name() string { return "graywolf" }

// State implements link.Link.
func (t *Transport) State() link.State {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// SetEnabled starts/stops health polling. A disabled transport reports
// Disconnected and refuses to send.
func (t *Transport) SetEnabled(on bool) {
	t.mu.Lock()
	t.enabled = on
	t.mu.Unlock()
	if !on {
		t.setState(link.Disconnected, "inactive")
	}
	t.kick()
}

// Reconfigure applies new settings and re-probes immediately.
func (t *Transport) Reconfigure(base, user, pass string, opts Options) {
	t.client.Reconfigure(base, user, pass)
	t.mu.Lock()
	t.opts = opts
	t.ids = map[string]int{} // beacon ids belong to the old server
	t.mu.Unlock()
	t.kick()
}

func (t *Transport) kick() {
	select {
	case t.wake <- struct{}{}:
	default:
	}
}

func (t *Transport) setState(st link.Status, detail string) {
	t.mu.Lock()
	prev := t.state
	t.state = link.State{Transport: "graywolf", Detail: detail}.WithStatus(st)
	cur := t.state
	t.mu.Unlock()
	if prev.Status != cur.Status || prev.Detail != cur.Detail {
		t.onState(cur)
	}
}

// Run polls Graywolf's health until ctx ends: every 15 s while up, every
// 5 s while down. Intended for its own goroutine.
func (t *Transport) Run(ctx context.Context) {
	for {
		t.mu.Lock()
		enabled := t.enabled
		t.mu.Unlock()
		wait := 15 * time.Second
		if enabled {
			if err := t.probe(ctx); err != nil {
				wait = 5 * time.Second
			}
		} else {
			wait = time.Hour
		}
		select {
		case <-ctx.Done():
			return
		case <-t.wake:
		case <-time.After(wait):
		}
	}
}

// probe checks reachability + login and updates the state.
func (t *Transport) probe(ctx context.Context) error {
	pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	wasUp := t.State().Status == link.Connected
	v, err := t.client.GetVersion(pctx)
	if err != nil {
		t.setState(link.Disconnected, "Graywolf not reachable at "+t.client.Base())
		if wasUp {
			t.log.Warn("graywolf unreachable", "err", err)
		}
		return err
	}
	if err := t.client.Login(pctx); err != nil {
		t.setState(link.Disconnected, strings.TrimPrefix(err.Error(), "graywolf: "))
		if wasUp {
			t.log.Warn("graywolf login failed", "err", err)
		}
		return err
	}
	if !wasUp {
		t.log.Info("graywolf connected", "url", t.client.Base(), "version", v.Version)
	}
	t.setState(link.Connected, "Graywolf "+v.Version+" at "+hostOf(t.client.Base()))
	return nil
}

func hostOf(base string) string {
	s := base
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	return strings.TrimRight(s, "/")
}

// Send implements link.Link: ensure a Graywolf beacon exists for the
// object, shape it as a live object (type object) or a kill (type custom
// carrying our exact info field), then fire it.
func (t *Transport) Send(ctx context.Context, p link.Packet) error {
	t.mu.Lock()
	enabled, st, opts := t.enabled, t.state.Status, t.opts
	t.mu.Unlock()
	if !enabled || st != link.Connected {
		return link.ErrNotConnected
	}
	b := t.beaconFor(p, opts)
	id, err := t.ensureBeacon(ctx, p.Object, b)
	if err != nil {
		return t.wrap(err)
	}
	if err := t.client.SendBeacon(ctx, id); err != nil {
		if IsNotFound(err) {
			// Deleted behind our back (e.g. in Graywolf's UI): recreate once.
			t.forget(p.Object)
			if id, err = t.ensureBeacon(ctx, p.Object, b); err == nil {
				err = t.client.SendBeacon(ctx, id)
			}
		}
		if err != nil {
			return t.wrap(err)
		}
	}
	return nil
}

// wrap turns a transport failure into ErrNotConnected when Graywolf went
// away, so the scheduler retries quietly; other errors are surfaced.
func (t *Transport) wrap(err error) error {
	var ae *APIError
	if !errors.As(err, &ae) {
		// network-level failure
		t.setState(link.Disconnected, strings.TrimPrefix(err.Error(), "graywolf: "))
		t.kick()
		return fmt.Errorf("%w: %v", link.ErrNotConnected, err)
	}
	return err
}

// beaconFor maps a packet onto Graywolf's beacon DTO.
func (t *Transport) beaconFor(p link.Packet, opts Options) Beacon {
	b := Beacon{
		Callsign:    p.Source.String(),
		Destination: p.Dest.String(),
		Path:        p.PathString(),
		Channel:     opts.Channel,
		SendPath:    opts.SendPath,
		Enabled:     false, // emcomm-objects owns the schedule; Graywolf only fires on /send
		Interval:    beaconInterval,
		SlotSeconds: -1,
		Latitude:    p.Latitude,
		Longitude:   p.Longitude,
	}
	if p.IntervalSeconds > 0 {
		b.Interval = p.IntervalSeconds
	}
	if p.Killed {
		// Graywolf has no "killed object" type; a custom beacon sends our
		// spec-exact ';NAME     _DDHHMMz…' info field verbatim. Comment must
		// stay empty (Graywolf appends it to custom_info).
		b.Type = "custom"
		b.ObjectName = p.Object
		b.CustomInfo = p.Info
		return b
	}
	b.Type = "object"
	b.ObjectName = p.Object
	b.Comment = p.Comment
	b.AltFt = p.AltitudeFt
	b.SymbolTable, b.Symbol, b.Overlay = splitSymbol(p.SymbolTab, p.SymbolCode)
	return b
}

// splitSymbol turns an APRS table byte into Graywolf's (table, symbol,
// overlay): an overlay character (A-Z, 0-9) means the alternate table with
// that overlay.
func splitSymbol(table, code byte) (string, string, string) {
	tbl := string(table)
	overlay := ""
	if (table >= 'A' && table <= 'Z') || (table >= '0' && table <= '9') {
		tbl = `\`
		overlay = string(table)
	}
	return tbl, string(code), overlay
}

// ensureBeacon returns the id of the Graywolf beacon for the object,
// creating or updating it so it matches b.
func (t *Transport) ensureBeacon(ctx context.Context, object string, b Beacon) (int, error) {
	t.mu.Lock()
	id, ok := t.ids[object]
	t.mu.Unlock()
	if ok {
		if _, err := t.client.UpdateBeacon(ctx, id, b); err == nil {
			return id, nil
		} else if !IsNotFound(err) {
			return 0, err
		}
		t.forget(object)
	}
	// Look for one we created in an earlier run.
	existing, err := t.client.ListBeacons(ctx)
	if err != nil {
		return 0, err
	}
	for _, e := range existing {
		if e.ObjectName == object && strings.EqualFold(e.Callsign, b.Callsign) && (e.Type == "object" || e.Type == "custom") {
			if _, err := t.client.UpdateBeacon(ctx, e.ID, b); err != nil {
				return 0, err
			}
			t.remember(object, e.ID)
			return e.ID, nil
		}
	}
	created, err := t.client.CreateBeacon(ctx, b)
	if err != nil {
		return 0, err
	}
	if created.ID == 0 {
		return 0, fmt.Errorf("graywolf: create beacon returned no id")
	}
	t.remember(object, created.ID)
	t.log.Debug("graywolf beacon created", "object", object, "id", created.ID)
	return created.ID, nil
}

func (t *Transport) remember(object string, id int) {
	t.mu.Lock()
	t.ids[object] = id
	t.mu.Unlock()
}

func (t *Transport) forget(object string) {
	t.mu.Lock()
	delete(t.ids, object)
	t.mu.Unlock()
}

// Retire implements link.Link: delete the Graywolf beacon for an object
// that is gone (deleted, or its kill sequence finished).
func (t *Transport) Retire(ctx context.Context, object string) error {
	t.mu.Lock()
	enabled, st := t.enabled, t.state.Status
	id, ok := t.ids[object]
	t.mu.Unlock()
	if !enabled || st != link.Connected {
		return nil
	}
	if !ok {
		existing, err := t.client.ListBeacons(ctx)
		if err != nil {
			return err
		}
		for _, e := range existing {
			if e.ObjectName == object && (e.Type == "object" || e.Type == "custom") {
				id, ok = e.ID, true
				break
			}
		}
	}
	if !ok {
		return nil
	}
	t.forget(object)
	if err := t.client.DeleteBeacon(ctx, id); err != nil {
		return err
	}
	t.log.Debug("graywolf beacon deleted", "object", object, "id", id)
	return nil
}
