package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/store"
)

func newStore(t *testing.T, objs ...store.Object) *store.Store {
	t.Helper()
	s := store.New(filepath.Join(t.TempDir(), "objects.json"))
	for _, o := range objs {
		if err := s.Upsert(o); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func TestDueNow(t *testing.T) {
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	// Never beaconed → due.
	o := store.Object{IntervalMinutes: 30}
	if !dueNow(o, now) {
		t.Error("never-beaconed should be due")
	}
	// Just beaconed → not due.
	o.LastBeacon = store.LooseTime{Time: now.Add(-5 * time.Minute)}
	if dueNow(o, now) {
		t.Error("recently beaconed should not be due")
	}
	// Beaconed exactly interval ago → due.
	o.LastBeacon = store.LooseTime{Time: now.Add(-30 * time.Minute)}
	if !dueNow(o, now) {
		t.Error("interval-ago beacon should be due")
	}
}

// recorder is a TransmitFunc that records every call.
type recorder struct {
	mu    sync.Mutex
	calls []recorderCall
	fail  atomic.Bool
}

type recorderCall struct {
	Object store.Object
	When   time.Time
}

func (r *recorder) TransmitFunc() TransmitFunc {
	return func(o store.Object, now time.Time) error {
		if r.fail.Load() {
			return errors.New("simulated failure")
		}
		r.mu.Lock()
		r.calls = append(r.calls, recorderCall{Object: o, When: now})
		r.mu.Unlock()
		return nil
	}
}

func (r *recorder) Calls() []recorderCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recorderCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func TestRun_BeaconsDueObjectsOnFirstTick(t *testing.T) {
	st := newStore(t,
		store.Object{
			ObjectName: "A", SymbolTable: "/", SymbolID: "r",
			Latitude: 33, Longitude: -84,
			IntervalMinutes: 30, Enabled: true,
			// LastBeacon zero → due.
		},
		store.Object{
			ObjectName: "B", SymbolTable: "/", SymbolID: "r",
			Latitude: 33, Longitude: -84,
			IntervalMinutes: 30, Enabled: false, // disabled, should not beacon
		},
	)
	r := &recorder{}
	sch := New(st, r.TransmitFunc(), Options{TickInterval: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	// Wait for at least one tick + immediate first check.
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done

	calls := r.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1 (only A is enabled and due)", len(calls))
	}
	if calls[0].Object.ObjectName != "A" {
		t.Errorf("beaconed wrong object: %s", calls[0].Object.ObjectName)
	}
	// LastBeacon should now be set.
	if got, _ := st.Get("A"); got.LastBeacon.IsZero() {
		t.Error("LastBeacon was not updated")
	}
}

func TestRun_DoesNotBeaconBeforeInterval(t *testing.T) {
	now := time.Now().UTC()
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		IntervalMinutes: 30, Enabled: true,
		LastBeacon: store.LooseTime{Time: now}, // just beaconed
	})
	r := &recorder{}
	sch := New(st, r.TransmitFunc(), Options{TickInterval: 30 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if calls := r.Calls(); len(calls) != 0 {
		t.Errorf("expected 0 beacons (not yet due), got %d", len(calls))
	}
}

func TestBeaconNow_Manual(t *testing.T) {
	now := time.Now().UTC()
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		IntervalMinutes: 30, Enabled: true,
		LastBeacon: store.LooseTime{Time: now}, // not naturally due
	})
	r := &recorder{}
	sch := New(st, r.TransmitFunc(), Options{TickInterval: time.Hour}) // effectively disable ticker

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	if err := sch.BeaconNow(ctx, "A"); err != nil {
		t.Fatalf("BeaconNow: %v", err)
	}
	cancel()
	<-done

	if calls := r.Calls(); len(calls) != 1 {
		t.Errorf("got %d calls, want 1", len(calls))
	}
}

func TestBeaconNow_Unknown(t *testing.T) {
	st := newStore(t)
	r := &recorder{}
	sch := New(st, r.TransmitFunc(), Options{TickInterval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	if err := sch.BeaconNow(ctx, "missing"); !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("got %v, want ErrObjectNotFound", err)
	}
	cancel()
	<-done
}

func TestRun_TransmitFailDoesNotUpdateLastBeacon(t *testing.T) {
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		IntervalMinutes: 30, Enabled: true,
	})
	r := &recorder{}
	r.fail.Store(true)
	sch := New(st, r.TransmitFunc(), Options{TickInterval: 30 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	// On failure, LastBeacon must remain zero so we retry next tick.
	if got, _ := st.Get("A"); !got.LastBeacon.IsZero() {
		t.Errorf("LastBeacon updated despite failure: %v", got.LastBeacon)
	}
}
