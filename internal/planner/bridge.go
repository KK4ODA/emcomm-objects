// Package planner links Emcomm Objects to EmComm Planner (emcommplanner.org):
// heard stations from Graywolf go up, queued APRS messages come down and
// are sent through Graywolf, and the deployment's sites can be pulled as
// objects. Everything is authenticated with a per-group bridge token that
// the planner issues once.
//
// The bridge is deliberately simple: one goroutine, a fixed interval, no
// local state beyond a since-cursor and a small set of message ids already
// handed to Graywolf. The planner side is idempotent (positions are keyed
// by callsign and time; messages are acknowledged by id).
package planner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
)

// Graywolf is the subset of the Graywolf client the bridge needs.
type Graywolf interface {
	ListStations(ctx context.Context, timerangeSeconds int, since string) ([]map[string]any, error)
	SendMessage(ctx context.Context, to, text string) error
	StationCallsign(ctx context.Context) (string, error)
}

// State is what the UI shows.
type State struct {
	Enabled        bool      `json:"enabled"`
	Connected      bool      `json:"connected"` // last planner call succeeded
	LastRun        time.Time `json:"last_run,omitempty"`
	LastOK         time.Time `json:"last_ok,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	StationsSent   int       `json:"stations_sent"`   // in the last cycle
	StationsTotal  int       `json:"stations_total"`  // since start
	MessagesSent   int       `json:"messages_sent"`   // since start
	MessagesFailed int       `json:"messages_failed"` // since start
	BridgeName     string    `json:"bridge_name,omitempty"`
}

// Bridge runs the sync loop.
type Bridge struct {
	gw      Graywolf
	http    *http.Client
	cfg     func() config.Config
	log     *slog.Logger
	wake    chan struct{}
	mu      sync.Mutex
	state   State
	since   string
	sent    map[string]time.Time // outbox ids handed to Graywolf, to avoid a double send if the ack fails
	onState func(State)
}

// New builds a bridge. cfg is read on every cycle so Settings changes apply live.
func New(gw Graywolf, cfg func() config.Config, log *slog.Logger, onState func(State)) *Bridge {
	return &Bridge{gw: gw, cfg: cfg, log: log, http: &http.Client{Timeout: 25 * time.Second}, wake: make(chan struct{}, 1), sent: map[string]time.Time{}, onState: onState}
}

// State returns a copy of the current state.
func (b *Bridge) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Wake runs a cycle now (after Settings change or a manual sync).
func (b *Bridge) Wake() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Run loops until ctx ends. A disabled bridge idles and re-reads the config
// every interval so enabling it in Settings takes effect without a restart.
func (b *Bridge) Run(ctx context.Context) {
	for {
		p := b.cfg().Planner
		interval := time.Duration(p.IntervalSeconds) * time.Second
		if interval < 10*time.Second {
			interval = 30 * time.Second
		}
		if p.Enabled {
			b.cycle(ctx, p)
		} else {
			b.setState(func(s *State) { *s = State{Enabled: false} })
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		case <-b.wake:
		}
	}
}

func (b *Bridge) setState(f func(*State)) {
	b.mu.Lock()
	f(&b.state)
	s := b.state
	b.mu.Unlock()
	if b.onState != nil {
		b.onState(s)
	}
}

// cycle does one round: stations up, messages down.
func (b *Bridge) cycle(ctx context.Context, p config.Planner) {
	var errs []string
	sentStations := 0
	if p.ForwardStations {
		n, err := b.forwardStations(ctx, p)
		if err != nil {
			errs = append(errs, "stations: "+err.Error())
		}
		sentStations = n
	}
	msgOK, msgFail, err := 0, 0, error(nil)
	if p.SendMessages {
		msgOK, msgFail, err = b.pumpOutbox(ctx, p)
		if err != nil {
			errs = append(errs, "outbox: "+err.Error())
		}
	}
	now := time.Now()
	b.setState(func(s *State) {
		s.Enabled = true
		s.LastRun = now
		s.StationsSent = sentStations
		s.StationsTotal += sentStations
		s.MessagesSent += msgOK
		s.MessagesFailed += msgFail
		if len(errs) == 0 {
			s.Connected = true
			s.LastOK = now
			s.LastError = ""
		} else {
			s.Connected = false
			s.LastError = strings.Join(errs, "; ")
		}
	})
	if len(errs) > 0 {
		b.log.Warn("planner sync problem", "err", strings.Join(errs, "; "))
	}
	// forget acknowledged message ids after an hour
	b.mu.Lock()
	for id, t := range b.sent {
		if now.Sub(t) > time.Hour {
			delete(b.sent, id)
		}
	}
	b.mu.Unlock()
}

// forwardStations asks Graywolf for stations heard since the last cycle and
// posts them to the planner. The first cycle sends the configured lookback.
func (b *Bridge) forwardStations(ctx context.Context, p config.Planner) (int, error) {
	b.mu.Lock()
	since := b.since
	b.mu.Unlock()
	timerange := p.LookbackSeconds
	if timerange <= 0 {
		timerange = 3600
	}
	stations, err := b.gw.ListStations(ctx, timerange, since)
	if err != nil {
		return 0, fmt.Errorf("graywolf: %w", err)
	}
	if len(stations) == 0 {
		return 0, nil
	}
	call, _ := b.gw.StationCallsign(ctx)
	var out struct {
		Received int `json:"received"`
		Stored   int `json:"stored"`
	}
	if err := b.call(ctx, p, http.MethodPost, "/aprs-ingest/stations", map[string]any{"station_call": call, "stations": stations}, &out); err != nil {
		return 0, err
	}
	// Advance the cursor to the newest last_heard we forwarded.
	newest := since
	for _, st := range stations {
		if lh, ok := st["last_heard"].(string); ok && lh > newest {
			newest = lh
		}
	}
	b.mu.Lock()
	b.since = newest
	b.mu.Unlock()
	return out.Received, nil
}

// pumpOutbox fetches queued messages and sends each through Graywolf,
// acknowledging success or failure so the planner can retry or give up.
func (b *Bridge) pumpOutbox(ctx context.Context, p config.Planner) (ok, failed int, err error) {
	var box struct {
		Messages []struct {
			ID         string `json:"id"`
			ToCallsign string `json:"to_callsign"`
			Text       string `json:"text"`
		} `json:"messages"`
	}
	if err := b.call(ctx, p, http.MethodGet, "/aprs-ingest/outbox", nil, &box); err != nil {
		return 0, 0, err
	}
	for _, m := range box.Messages {
		b.mu.Lock()
		_, dup := b.sent[m.ID]
		b.mu.Unlock()
		if dup {
			continue
		}
		sendErr := b.gw.SendMessage(ctx, m.ToCallsign, m.Text)
		b.mu.Lock()
		b.sent[m.ID] = time.Now()
		b.mu.Unlock()
		ack := map[string]any{"id": m.ID, "ok": sendErr == nil}
		if sendErr != nil {
			ack["error"] = sendErr.Error()
			failed++
			b.log.Warn("aprs message failed", "to", m.ToCallsign, "err", sendErr)
		} else {
			ok++
			b.log.Info("aprs message sent", "to", m.ToCallsign, "text", m.Text)
		}
		if err := b.call(ctx, p, http.MethodPost, "/aprs-ingest/outbox/ack", ack, nil); err != nil {
			// The send happened; keep the id in `sent` so a retry does not re-send.
			return ok, failed, err
		}
	}
	return ok, failed, nil
}

// Test checks the token against the planner without touching Graywolf.
func (b *Bridge) Test(ctx context.Context, p config.Planner) (map[string]any, error) {
	var out map[string]any
	if err := b.call(ctx, p, http.MethodGet, "/aprs-ingest/ping", nil, &out); err != nil {
		return nil, err
	}
	if name, _ := out["bridge"].(string); name != "" {
		b.setState(func(s *State) { s.BridgeName = name })
	}
	return out, nil
}

// FetchObjects pulls the active deployment's sites as objects (Pinpoint shape).
func (b *Bridge) FetchObjects(ctx context.Context, p config.Planner, deployment string) ([]map[string]any, string, error) {
	if deployment == "" {
		deployment = "active"
	}
	var out struct {
		Deployment struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"deployment"`
		Objects []map[string]any `json:"objects"`
	}
	if err := b.call(ctx, p, http.MethodGet, "/aprs-ingest/objects?deployment="+deployment, nil, &out); err != nil {
		return nil, "", err
	}
	return out.Objects, out.Deployment.Name, nil
}

// call performs one authenticated request against the planner functions base.
func (b *Bridge) call(ctx context.Context, p config.Planner, method, path string, body any, out any) error {
	base := strings.TrimRight(strings.TrimSpace(p.URL), "/")
	if base == "" {
		return errors.New("planner URL is empty")
	}
	if strings.TrimSpace(p.Token) == "" {
		return errors.New("planner token is empty")
	}
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(p.Token))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
		}
		if len(msg) > 200 {
			msg = msg[:200]
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("planner rejected the token (revoked or mistyped)")
		}
		return fmt.Errorf("planner HTTP %d: %s", resp.StatusCode, msg)
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("planner: decode %s: %w", path, err)
		}
	}
	return nil
}
