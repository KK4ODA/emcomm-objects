// Package scheduler decides when each object in the store should beacon next
// and invokes a Transmit callback when the time arrives.
//
// Three behaviors per tick:
//  1. Live objects past their IntervalMinutes since LastBeacon → live beacon
//  2. Live objects past their ExpiresAt → auto-transition to killed, start
//     kill sequence (or silent-kill if never beaconed)
//  3. Killed objects with KillBeaconsLeft > 0 and past KillInterval since
//     LastBeacon → kill beacon (decrement counter)
//
// The scheduler is deliberately decoupled from KISS and AX.25 wiring: it
// just calls TransmitLive or TransmitKilled. The caller wires those to a
// real KISS sender or, in tests, to a recorder.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/store"
)

// TransmitFunc builds and sends an APRS object beacon. Implementations are
// expected to construct the APRS packet, frame it as AX.25 UI, KISS-frame it,
// and write to the connection. Returning an error skips the LastBeacon
// update so the scheduler will retry on the next tick.
type TransmitFunc func(o store.Object, now time.Time) error

// Defaults for kill cadence. The kill interval must exceed Graywolf's TX
// dedup window (30s) or duplicate kill packets get silently dropped.
const (
	DefaultKillBeaconCount    = 3
	DefaultKillBeaconInterval = 35 * time.Second
)

// Scheduler periodically inspects the object store and triggers beacons.
type Scheduler struct {
	store          *store.Store
	transmitLive   TransmitFunc
	transmitKilled TransmitFunc
	tick           time.Duration
	killInterval   time.Duration
	killCount      int
	log            *slog.Logger

	mu      sync.Mutex
	reqs    chan request
}

// Options configures a Scheduler.
type Options struct {
	// TickInterval is how often to check whether any object is due.
	// Defaults to 10 seconds. Choose at least 2x finer than the shortest
	// IntervalMinutes you want to support.
	TickInterval time.Duration
	// KillBeaconInterval is the spacing between successive kill packets.
	// Must exceed any downstream TX dedup window (Graywolf is 30s, so 35s
	// is the safe default).
	KillBeaconInterval time.Duration
	// KillBeaconCount is how many kill packets are sent per "death".
	// 3 is the conventional APRS reliability target.
	KillBeaconCount int
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// New constructs a Scheduler. txLive must be non-nil; txKilled may be nil
// if you don't care about kill functionality (kills will fail silently).
func New(s *store.Store, txLive, txKilled TransmitFunc, opts Options) *Scheduler {
	if opts.TickInterval <= 0 {
		opts.TickInterval = 10 * time.Second
	}
	if opts.KillBeaconInterval <= 0 {
		opts.KillBeaconInterval = DefaultKillBeaconInterval
	}
	if opts.KillBeaconCount <= 0 {
		opts.KillBeaconCount = DefaultKillBeaconCount
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Scheduler{
		store:          s,
		transmitLive:   txLive,
		transmitKilled: txKilled,
		tick:           opts.TickInterval,
		killInterval:   opts.KillBeaconInterval,
		killCount:      opts.KillBeaconCount,
		log:            opts.Logger,
		reqs:           make(chan request, 16),
	}
}

// Run loops until ctx is cancelled, processing scheduled work each tick
// and handling manual requests in between.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.tick)
	defer t.Stop()
	s.checkAll(time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.checkAll(now)
		case req := <-s.reqs:
			req.reply <- s.handleRequest(req)
		}
	}
}

// BeaconNow queues a manual LIVE beacon for the named object. Returns the
// result once the scheduler has processed it. Returns ErrObjectKilled if
// the object is in killed state — you can't live-beacon a dead object.
func (s *Scheduler) BeaconNow(ctx context.Context, name string) error {
	return s.submit(ctx, request{kind: reqBeacon, name: name})
}

// KillNow queues a manual KILL for the named object. The object transitions
// to killed status with KillBeaconCount packets remaining; the first kill
// fires immediately, the rest drain over the next ticks.
//
// Returns ErrAlreadyKilled if the object is already in killed state.
// Returns ErrNeverBeaconed if the object was never live-beaconed — sending
// a kill packet for an unknown object would create a brief ghost on
// receivers, which is undesirable. In that case the object is silently
// marked killed in the store (no packets sent).
func (s *Scheduler) KillNow(ctx context.Context, name string) error {
	return s.submit(ctx, request{kind: reqKill, name: name})
}

// ReviveNow brings a killed object back to live status, clearing ExpiresAt
// (so a past expiry doesn't immediately re-kill it) and zeroing LastBeacon
// so the next scheduler tick fires a fresh live beacon. The object will
// re-appear as a live object on receivers that see the next live packet.
//
// Returns store.ErrNotKilled if the object is currently live (revive is
// a state transition, not a no-op).
func (s *Scheduler) ReviveNow(ctx context.Context, name string) error {
	return s.submit(ctx, request{kind: reqRevive, name: name})
}

// ErrObjectNotFound is returned for unknown object names.
var ErrObjectNotFound = errors.New("scheduler: object not found")

// ErrObjectKilled is returned when BeaconNow is called on a killed object.
var ErrObjectKilled = errors.New("scheduler: object is killed; cannot beacon")

const (
	reqBeacon = "beacon"
	reqKill   = "kill"
	reqRevive = "revive"
)

type request struct {
	kind  string
	name  string
	reply chan error
}

func (s *Scheduler) submit(ctx context.Context, r request) error {
	r.reply = make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.reqs <- r:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-r.reply:
		return err
	}
}

func (s *Scheduler) handleRequest(r request) error {
	switch r.kind {
	case reqBeacon:
		return s.beaconLive(r.name, time.Now(), true)
	case reqKill:
		return s.startKill(r.name, time.Now())
	case reqRevive:
		return s.revive(r.name)
	default:
		return fmt.Errorf("unknown request kind %q", r.kind)
	}
}

// revive flips a killed object back to live and persists. The next checkAll
// tick will fire a live beacon (LastBeacon was reset to zero by Resurrect).
func (s *Scheduler) revive(name string) error {
	if err := s.store.Resurrect(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
		}
		return err
	}
	s.log.Info("revived", "object", name)
	if err := s.store.Save(); err != nil {
		s.log.Error("persist revive failed", "object", name, "err", err)
	}
	return nil
}

// checkAll iterates the current object list and processes each. For any
// object we process the highest-priority lifecycle event applicable.
func (s *Scheduler) checkAll(now time.Time) {
	for _, o := range s.store.List() {
		if err := s.processOne(o, now); err != nil {
			s.log.Warn("scheduled work failed",
				"object", o.ObjectName, "err", err)
		}
	}
}

// processOne handles all lifecycle decisions for a single object on a tick.
func (s *Scheduler) processOne(o store.Object, now time.Time) error {
	// 1. Killed objects: drain remaining kill beacons.
	if o.IsKilled() {
		if o.KillBeaconsLeft <= 0 {
			return nil
		}
		if !o.LastBeacon.IsZero() && now.Sub(o.LastBeacon.Time) < s.killInterval {
			return nil
		}
		return s.sendKill(o, now)
	}

	// 2. Live but past expiry → auto-kill.
	if !o.ExpiresAt.IsZero() && !now.Before(o.ExpiresAt.Time) {
		if o.NeverBeaconed() {
			s.log.Info("auto-expire silent (never beaconed)", "object", o.ObjectName)
			if err := s.store.StartKill(o.ObjectName, 0, now); err != nil {
				return fmt.Errorf("silent kill: %w", err)
			}
			return s.store.Save()
		}
		s.log.Info("auto-expire", "object", o.ObjectName, "kill_count", s.killCount)
		if err := s.store.StartKill(o.ObjectName, s.killCount, now); err != nil {
			return fmt.Errorf("auto-expire StartKill: %w", err)
		}
		if err := s.store.Save(); err != nil {
			s.log.Error("persist auto-expire failed", "object", o.ObjectName, "err", err)
		}
		// Re-fetch the now-killed object and fire its first kill immediately.
		if killed, ok := s.store.Get(o.ObjectName); ok {
			return s.sendKill(killed, now)
		}
		return nil
	}

	// 3. Normal live beacon.
	if !o.Enabled || o.IntervalMinutes <= 0 {
		return nil
	}
	if !dueNow(o, now) {
		return nil
	}
	return s.beaconLive(o.ObjectName, now, false)
}

// dueNow reports whether o is due for a scheduled live beacon.
func dueNow(o store.Object, now time.Time) bool {
	if o.LastBeacon.IsZero() {
		return true
	}
	interval := time.Duration(o.IntervalMinutes) * time.Minute
	return now.Sub(o.LastBeacon.Time) >= interval
}

// beaconLive sends a live beacon for the named object and updates LastBeacon
// on success. manual=true bypasses the Enabled gate but not the IsKilled
// gate (you can't live-beacon a killed object).
func (s *Scheduler) beaconLive(name string, now time.Time, manual bool) error {
	o, ok := s.store.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
	}
	if o.IsKilled() {
		return fmt.Errorf("%w: %s", ErrObjectKilled, name)
	}
	if !manual && !o.Enabled {
		return errors.New("object is disabled")
	}
	src := "scheduled"
	if manual {
		src = "manual"
	}
	s.log.Info("beacon", "object", o.ObjectName, "source", src, "kind", "live")
	if err := s.transmitLive(o, now); err != nil {
		return err
	}
	s.store.MarkBeaconed(o.ObjectName, now)
	if err := s.store.Save(); err != nil {
		s.log.Error("persist LastBeacon failed", "object", o.ObjectName, "err", err)
	}
	return nil
}

// startKill transitions the object to killed status (if not already) and
// fires the first kill packet immediately. Subsequent kill packets are
// fired by checkAll on later ticks.
func (s *Scheduler) startKill(name string, now time.Time) error {
	o, ok := s.store.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
	}
	if o.IsKilled() {
		return store.ErrAlreadyKilled
	}
	if o.NeverBeaconed() {
		// Silent kill: no packet, just stop scheduling.
		if err := s.store.StartKill(o.ObjectName, 0, now); err != nil {
			return err
		}
		if err := s.store.Save(); err != nil {
			s.log.Error("persist silent-kill failed", "object", o.ObjectName, "err", err)
		}
		s.log.Info("manual kill silent (never beaconed)", "object", o.ObjectName)
		return store.ErrNeverBeaconed
	}
	if err := s.store.StartKill(o.ObjectName, s.killCount, now); err != nil {
		return err
	}
	if err := s.store.Save(); err != nil {
		s.log.Error("persist start-kill failed", "object", o.ObjectName, "err", err)
	}
	// Fire first kill packet immediately.
	killed, _ := s.store.Get(o.ObjectName)
	return s.sendKill(killed, now)
}

// sendKill transmits one kill packet and decrements the kill counter.
func (s *Scheduler) sendKill(o store.Object, now time.Time) error {
	if s.transmitKilled == nil {
		return errors.New("scheduler: no killed-transmit function configured")
	}
	s.log.Info("beacon", "object", o.ObjectName, "kind", "killed",
		"remaining", o.KillBeaconsLeft)
	if err := s.transmitKilled(o, now); err != nil {
		return err
	}
	s.store.DecrementKillBeacon(o.ObjectName, now)
	if err := s.store.Save(); err != nil {
		s.log.Error("persist kill beacon failed", "object", o.ObjectName, "err", err)
	}
	return nil
}
