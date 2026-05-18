// Package scheduler decides when each object in the store should beacon next
// and invokes a Transmit callback when the time arrives.
//
// The scheduler is deliberately decoupled from KISS and AX.25 wiring: it
// just calls Transmit(object, now). The caller wires that to a real KISS
// transmitter or, in tests, to a recorder.
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

// Scheduler periodically inspects the object store and triggers beacons for
// any object whose IntervalMinutes has elapsed since its LastBeacon.
type Scheduler struct {
	store    *store.Store
	transmit TransmitFunc
	tick     time.Duration
	log      *slog.Logger

	mu        sync.Mutex
	manualReq chan beaconRequest
}

// Options configures a Scheduler.
type Options struct {
	// TickInterval is how often to check whether any object is due.
	// Defaults to 10 seconds. Choose a value at least 2x finer than the
	// shortest IntervalMinutes you want to support.
	TickInterval time.Duration
	// Logger; defaults to slog.Default().
	Logger *slog.Logger
}

// New constructs a Scheduler.
func New(s *store.Store, tx TransmitFunc, opts Options) *Scheduler {
	if opts.TickInterval <= 0 {
		opts.TickInterval = 10 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Scheduler{
		store:     s,
		transmit:  tx,
		tick:      opts.TickInterval,
		log:       opts.Logger,
		manualReq: make(chan beaconRequest, 16),
	}
}

// Run loops until ctx is cancelled, beaconing objects when due and handling
// manual BeaconNow requests.
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
		case req := <-s.manualReq:
			err := s.beaconOne(req.name, time.Now(), true)
			req.reply <- err
		}
	}
}

// BeaconNow queues a manual beacon for the named object. Returns the result
// once the scheduler has processed it.
func (s *Scheduler) BeaconNow(ctx context.Context, name string) error {
	reply := make(chan error, 1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case s.manualReq <- beaconRequest{name: name, reply: reply}:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

// ErrObjectNotFound indicates BeaconNow was called with an unknown name.
var ErrObjectNotFound = errors.New("scheduler: object not found")

type beaconRequest struct {
	name  string
	reply chan error
}

// checkAll iterates the current object list and beacons any that are due.
func (s *Scheduler) checkAll(now time.Time) {
	for _, o := range s.store.List() {
		if !o.Enabled || o.IntervalMinutes <= 0 {
			continue
		}
		if !dueNow(o, now) {
			continue
		}
		if err := s.beaconOne(o.ObjectName, now, false); err != nil {
			s.log.Warn("scheduled beacon failed",
				"object", o.ObjectName, "err", err)
		}
	}
}

// dueNow reports whether o is due for a scheduled beacon at the given time.
func dueNow(o store.Object, now time.Time) bool {
	if o.LastBeacon.IsZero() {
		return true
	}
	interval := time.Duration(o.IntervalMinutes) * time.Minute
	return now.Sub(o.LastBeacon.Time) >= interval
}

// beaconOne sends a single object beacon and, on success, updates LastBeacon
// in the store and persists the file.
func (s *Scheduler) beaconOne(name string, now time.Time, manual bool) error {
	o, ok := s.store.Get(name)
	if !ok {
		return fmt.Errorf("%w: %s", ErrObjectNotFound, name)
	}
	if !manual && !o.Enabled {
		return errors.New("object is disabled")
	}
	src := "scheduled"
	if manual {
		src = "manual"
	}
	s.log.Info("beacon", "object", o.ObjectName, "source", src)
	if err := s.transmit(o, now); err != nil {
		return err
	}
	s.store.MarkBeaconed(o.ObjectName, now)
	if err := s.store.Save(); err != nil {
		// Beacon succeeded but persistence failed — log loudly so we don't
		// silently re-beacon on next tick.
		s.log.Error("persist LastBeacon failed", "object", o.ObjectName, "err", err)
	}
	return nil
}
