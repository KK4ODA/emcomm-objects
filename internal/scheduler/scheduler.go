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
// The scheduler is decoupled from the transport: it calls TransmitLive,
// TransmitKilled and (optionally) Retire. The caller wires those to a real
// sender or, in tests, to a recorder.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

// TransmitFunc builds and sends an APRS object beacon. Returning an error
// skips the LastBeacon update so the scheduler retries on the next tick.
type TransmitFunc func(o store.Object, now time.Time) error

// Defaults for kill cadence. The kill interval must exceed Graywolf's TX
// dedup window (30s) or duplicate kill packets get silently dropped.
const (
	DefaultKillBeaconCount    = 3
	DefaultKillBeaconInterval = 35 * time.Second
	DefaultTickInterval       = 10 * time.Second
)

// Scheduler periodically inspects the object store and triggers beacons.
type Scheduler struct {
	store          *store.Store
	transmitLive   TransmitFunc
	transmitKilled TransmitFunc
	retire         func(name string) error
	readyFn        func() (bool, string)
	deferred       bool
	tick           time.Duration
	killInterval   time.Duration
	killCount      int
	log            *slog.Logger
	reqs           chan request
	kick           chan struct{}
}

// Options configures a Scheduler.
type Options struct {
	// TickInterval is how often to check whether any object is due
	// (default 10 s). Keep it at least 2x finer than the shortest interval.
	TickInterval time.Duration
	// KillBeaconInterval is the spacing between successive kill packets
	// (default 35 s, must exceed any downstream TX dedup window).
	KillBeaconInterval time.Duration
	// KillBeaconCount is how many kill packets are sent per "death" (default 3).
	KillBeaconCount int
	// Retire is called once an object's kill sequence has fully drained, so
	// the transport can drop per-object state. May be nil.
	Retire func(name string) error
	// Ready, when set, is consulted before each scheduled pass; false skips
	// the pass (transport down, station not configured). The reason is
	// logged once. Manual requests are not gated.
	Ready func() (ok bool, reason string)
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// New constructs a Scheduler. txLive must be non-nil; txKilled may be nil
// if you don't care about kill functionality (kills will fail).
func New(s *store.Store, txLive, txKilled TransmitFunc, opts Options) *Scheduler {
	if opts.TickInterval <= 0 {
		opts.TickInterval = DefaultTickInterval
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
		retire:         opts.Retire,
		readyFn:        opts.Ready,
		tick:           opts.TickInterval,
		killInterval:   opts.KillBeaconInterval,
		killCount:      opts.KillBeaconCount,
		log:            opts.Logger,
		reqs:           make(chan request, 16),
		kick:           make(chan struct{}, 1),
	}
}

// KillBeaconCount returns the configured number of kill packets.
func (s *Scheduler) KillBeaconCount() int { return s.killCount }

// KillBeaconInterval returns the configured spacing of kill packets.
func (s *Scheduler) KillBeaconInterval() time.Duration { return s.killInterval }

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
		case <-s.kick:
			s.checkAll(time.Now())
		case req := <-s.reqs:
			req.reply <- s.handleRequest(req)
		}
	}
}

// Wake makes the scheduler run a check right away (e.g. the transport just
// came up, or an object was edited).
func (s *Scheduler) Wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// BeaconNow queues a manual LIVE beacon for the named object. Returns
// ErrObjectKilled if the object is killed.
func (s *Scheduler) BeaconNow(ctx context.Context, name string) error {
	return s.submit(ctx, request{kind: reqBeacon, name: name})
}

// KillNow queues a manual KILL for the named object: killed status with
// KillBeaconCount packets remaining; the first fires immediately, the rest
// drain over the next ticks. Returns store.ErrAlreadyKilled if already
// killed and store.ErrNeverBeaconed if the object was never on the air (it
// is then marked killed locally without sending anything).
func (s *Scheduler) KillNow(ctx context.Context, name string) error {
	return s.submit(ctx, request{kind: reqKill, name: name})
}

// ReviveNow brings a killed object back to live status; the next tick
// fires a fresh live beacon. Returns store.ErrNotKilled if it is live.
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

func (s *Scheduler) revive(name string) error {
	if err := s.store.Resurrect(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
		}
		return err
	}
	s.log.Info("revived", "object", name)
	s.persist("revive", name)
	s.Wake()
	return nil
}

// checkAll processes every object once. When the transport is down (or
// the station is not configured yet) objects are skipped with a single
// log line until it comes back.
func (s *Scheduler) checkAll(now time.Time) {
	if !s.ready() {
		return
	}
	for _, o := range s.store.List() {
		if err := s.processOne(o, now); err != nil {
			if errors.Is(err, link.ErrNotConnected) {
				if !s.deferred {
					s.log.Info("beacons deferred", "reason", err)
					s.deferred = true
				}
				return
			}
			s.log.Warn("scheduled work failed", "object", o.ObjectName, "err", err)
		}
	}
	if s.deferred {
		s.log.Info("beacons resumed")
		s.deferred = false
	}
}

// ready consults the optional Ready hook (transport up and station set).
func (s *Scheduler) ready() bool {
	if s.readyFn == nil {
		return true
	}
	ok, reason := s.readyFn()
	if !ok {
		if !s.deferred {
			s.log.Info("beacons deferred", "reason", reason)
			s.deferred = true
		}
		return false
	}
	if s.deferred {
		s.log.Info("beacons resumed")
		s.deferred = false
	}
	return true
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
			s.persist("auto-expire", o.ObjectName)
			return nil
		}
		s.log.Info("auto-expire", "object", o.ObjectName, "kill_count", s.killCount)
		if err := s.store.StartKill(o.ObjectName, s.killCount, now); err != nil {
			return fmt.Errorf("auto-expire StartKill: %w", err)
		}
		s.persist("auto-expire", o.ObjectName)
		if killed, ok := s.store.Get(o.ObjectName); ok {
			return s.sendKill(killed, now)
		}
		return nil
	}

	// 3. Normal live beacon.
	if !o.Enabled || o.IntervalMinutes <= 0 || !dueNow(o, now) {
		return nil
	}
	return s.beaconLive(o.ObjectName, now, false)
}

// dueNow reports whether o is due for a scheduled live beacon.
func dueNow(o store.Object, now time.Time) bool {
	if o.LastBeacon.IsZero() {
		return true
	}
	return now.Sub(o.LastBeacon.Time) >= time.Duration(o.IntervalMinutes)*time.Minute
}

// beaconLive sends a live beacon and updates LastBeacon on success.
// manual=true bypasses the Enabled gate but not the IsKilled gate.
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
	s.persist("LastBeacon", o.ObjectName)
	return nil
}

// startKill transitions the object to killed status and fires the first
// kill packet immediately; later packets go out on ticks.
func (s *Scheduler) startKill(name string, now time.Time) error {
	o, ok := s.store.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
	}
	if o.IsKilled() {
		return store.ErrAlreadyKilled
	}
	if o.NeverBeaconed() {
		if err := s.store.StartKill(o.ObjectName, 0, now); err != nil {
			return err
		}
		s.persist("silent-kill", o.ObjectName)
		s.log.Info("manual kill silent (never beaconed)", "object", o.ObjectName)
		return store.ErrNeverBeaconed
	}
	if err := s.store.StartKill(o.ObjectName, s.killCount, now); err != nil {
		return err
	}
	s.persist("start-kill", o.ObjectName)
	killed, _ := s.store.Get(o.ObjectName)
	return s.sendKill(killed, now)
}

// sendKill transmits one kill packet and decrements the kill counter; when
// the counter reaches zero the transport is told to retire the object.
func (s *Scheduler) sendKill(o store.Object, now time.Time) error {
	if s.transmitKilled == nil {
		return errors.New("scheduler: no killed-transmit function configured")
	}
	s.log.Info("beacon", "object", o.ObjectName, "kind", "killed", "remaining", o.KillBeaconsLeft)
	if err := s.transmitKilled(o, now); err != nil {
		return err
	}
	s.store.DecrementKillBeacon(o.ObjectName, now)
	s.persist("kill beacon", o.ObjectName)
	if after, ok := s.store.Get(o.ObjectName); ok && after.KillBeaconsLeft == 0 && s.retire != nil {
		if err := s.retire(o.ObjectName); err != nil {
			s.log.Warn("retire after kill failed", "object", o.ObjectName, "err", err)
		}
	}
	return nil
}

func (s *Scheduler) persist(what, name string) {
	if err := s.store.Save(); err != nil {
		s.log.Error("persist failed", "what", what, "object", name, "err", err)
	}
}
