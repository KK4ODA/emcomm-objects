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
	o := store.Object{IntervalMinutes: 30}
	if !dueNow(o, now) {
		t.Error("never-beaconed should be due")
	}
	o.LastBeacon = store.LooseTime{Time: now.Add(-5 * time.Minute)}
	if dueNow(o, now) {
		t.Error("recently beaconed should not be due")
	}
	o.LastBeacon = store.LooseTime{Time: now.Add(-30 * time.Minute)}
	if !dueNow(o, now) {
		t.Error("interval-ago beacon should be due")
	}
}

// recorder tracks live and killed packet emissions independently.
type recorder struct {
	mu     sync.Mutex
	live   []recorded
	killed []recorded
	fail   atomic.Bool
}

type recorded struct {
	Object store.Object
	When   time.Time
}

func (r *recorder) Live() TransmitFunc {
	return func(o store.Object, now time.Time) error {
		if r.fail.Load() {
			return errors.New("simulated failure")
		}
		r.mu.Lock()
		r.live = append(r.live, recorded{Object: o, When: now})
		r.mu.Unlock()
		return nil
	}
}
func (r *recorder) Killed() TransmitFunc {
	return func(o store.Object, now time.Time) error {
		if r.fail.Load() {
			return errors.New("simulated failure")
		}
		r.mu.Lock()
		r.killed = append(r.killed, recorded{Object: o, When: now})
		r.mu.Unlock()
		return nil
	}
}
func (r *recorder) LiveCount() int    { r.mu.Lock(); defer r.mu.Unlock(); return len(r.live) }
func (r *recorder) KilledCount() int  { r.mu.Lock(); defer r.mu.Unlock(); return len(r.killed) }

// helper: spin up Run for the duration, then cancel
func runFor(t *testing.T, sch *Scheduler, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	time.Sleep(d)
	cancel()
	<-done
}

func TestRun_BeaconsDueLiveObjects(t *testing.T) {
	st := newStore(t,
		store.Object{
			ObjectName: "A", SymbolTable: "/", SymbolID: "r",
			Latitude: 33, Longitude: -84,
			IntervalMinutes: 30, Enabled: true,
		},
		store.Object{
			ObjectName: "B", SymbolTable: "/", SymbolID: "r",
			IntervalMinutes: 30, Enabled: false, // disabled
		},
	)
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: 50 * time.Millisecond})
	runFor(t, sch, 150*time.Millisecond)

	if r.LiveCount() != 1 {
		t.Errorf("got %d live beacons, want 1 (only A enabled)", r.LiveCount())
	}
	if r.KilledCount() != 0 {
		t.Errorf("unexpected kill beacons: %d", r.KilledCount())
	}
}

func TestKillNow_FiresFirstImmediatelyAndDrains(t *testing.T) {
	now := time.Now().UTC()
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Latitude: 33, Longitude: -84,
		IntervalMinutes: 30, Enabled: true,
		LastBeacon: store.LooseTime{Time: now.Add(-time.Minute)}, // was live
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{
		TickInterval:       20 * time.Millisecond,
		KillBeaconInterval: 30 * time.Millisecond, // fast for test
		KillBeaconCount:    3,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	// First kill fires synchronously on KillNow.
	if err := sch.KillNow(ctx, "A"); err != nil {
		t.Fatalf("KillNow: %v", err)
	}
	if r.KilledCount() != 1 {
		t.Errorf("after KillNow: got %d kill beacons, want 1", r.KilledCount())
	}
	// Wait for the 2 remaining kills to drain.
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done

	if got := r.KilledCount(); got != 3 {
		t.Errorf("total kill beacons: got %d, want 3", got)
	}
	// And no more live beacons after the kill.
	if got := r.LiveCount(); got != 0 {
		t.Errorf("unexpected live beacons after kill: %d", got)
	}
	// Object should be in killed status with KillBeaconsLeft = 0.
	if o, _ := st.Get("A"); !o.IsKilled() || o.KillBeaconsLeft != 0 {
		t.Errorf("end state wrong: status=%q left=%d", o.Status, o.KillBeaconsLeft)
	}
}

func TestKillNow_NeverBeaconedIsSilent(t *testing.T) {
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Latitude: 33, Longitude: -84,
		IntervalMinutes: 0, Enabled: false, // not auto-beacon eligible
		// LastBeacon is zero — the case we're testing.
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	err := sch.KillNow(ctx, "A")
	if !errors.Is(err, store.ErrNeverBeaconed) {
		t.Errorf("got %v, want ErrNeverBeaconed", err)
	}
	cancel()
	<-done

	if r.KilledCount() != 0 {
		t.Errorf("expected 0 kill beacons (silent), got %d", r.KilledCount())
	}
	// But object must be marked killed locally.
	if o, _ := st.Get("A"); !o.IsKilled() {
		t.Error("object should be marked killed even though no packets sent")
	}
}

func TestKillNow_AlreadyKilled(t *testing.T) {
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		LastBeacon: store.LooseTime{Time: time.Now()},
		Status:     store.StatusKilled, KillBeaconsLeft: 2,
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	if err := sch.KillNow(ctx, "A"); !errors.Is(err, store.ErrAlreadyKilled) {
		t.Errorf("got %v, want ErrAlreadyKilled", err)
	}
	cancel()
	<-done
}

func TestBeaconNow_RejectsKilledObject(t *testing.T) {
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Enabled: true, IntervalMinutes: 30,
		Status: store.StatusKilled,
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	if err := sch.BeaconNow(ctx, "A"); !errors.Is(err, ErrObjectKilled) {
		t.Errorf("got %v, want ErrObjectKilled", err)
	}
	cancel()
	<-done

	if r.LiveCount() != 0 {
		t.Errorf("unexpected live beacons: %d", r.LiveCount())
	}
}

func TestAutoExpire_PastExpiryTriggersKillSequence(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Latitude: 33, Longitude: -84,
		Enabled: true, IntervalMinutes: 30,
		LastBeacon: store.LooseTime{Time: time.Now().Add(-2 * time.Minute)}, // was live
		ExpiresAt:  store.LooseTime{Time: past},
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{
		TickInterval:       20 * time.Millisecond,
		KillBeaconInterval: 30 * time.Millisecond,
		KillBeaconCount:    3,
	})

	runFor(t, sch, 200*time.Millisecond)

	if r.LiveCount() != 0 {
		t.Errorf("expected 0 live beacons (object expired), got %d", r.LiveCount())
	}
	if r.KilledCount() != 3 {
		t.Errorf("expected 3 kill beacons, got %d", r.KilledCount())
	}
	if o, _ := st.Get("A"); !o.IsKilled() {
		t.Errorf("object should be killed")
	}
}

func TestAutoExpire_NeverBeaconedIsSilent(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Enabled: true, IntervalMinutes: 30,
		ExpiresAt: store.LooseTime{Time: past},
		// LastBeacon zero.
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{
		TickInterval:       20 * time.Millisecond,
		KillBeaconInterval: 30 * time.Millisecond,
		KillBeaconCount:    3,
	})
	runFor(t, sch, 100*time.Millisecond)

	if r.LiveCount() != 0 || r.KilledCount() != 0 {
		t.Errorf("expected zero packets (silent expire), got live=%d killed=%d",
			r.LiveCount(), r.KilledCount())
	}
	if o, _ := st.Get("A"); !o.IsKilled() {
		t.Error("expired-never-beaconed should be marked killed")
	}
}

func TestAutoExpire_FutureExpiryDoesNotTrigger(t *testing.T) {
	future := time.Now().Add(time.Hour)
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Enabled: true, IntervalMinutes: 30,
		Latitude: 33, Longitude: -84,
		ExpiresAt: store.LooseTime{Time: future},
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: 30 * time.Millisecond})
	runFor(t, sch, 100*time.Millisecond)

	if r.KilledCount() != 0 {
		t.Errorf("future-expiry should not trigger kill, got %d", r.KilledCount())
	}
	if r.LiveCount() != 1 {
		t.Errorf("live beacon should fire normally, got %d", r.LiveCount())
	}
}

func TestKillSpacing_RespectsKillInterval(t *testing.T) {
	// Verify scheduler doesn't fire two kills closer than KillBeaconInterval.
	now := time.Now().UTC()
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		LastBeacon:      store.LooseTime{Time: now},
		Status:          store.StatusKilled,
		KillBeaconsLeft: 3,
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{
		TickInterval:       10 * time.Millisecond,
		KillBeaconInterval: 100 * time.Millisecond, // big enough to assert
		KillBeaconCount:    3,
	})
	runFor(t, sch, 50*time.Millisecond) // less than killInterval

	if r.KilledCount() != 0 {
		t.Errorf("should not have fired kill within interval, got %d", r.KilledCount())
	}
}

func TestReviveNow_RestoresLifeAndFiresLiveBeaconOnNextTick(t *testing.T) {
	now := time.Now().UTC()
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Latitude: 33, Longitude: -84,
		IntervalMinutes: 30, Enabled: true,
		Status:          store.StatusKilled,
		KillBeaconsLeft: 0,
		KilledAt:        store.LooseTime{Time: now.Add(-time.Minute)},
		ExpiresAt:       store.LooseTime{Time: now.Add(-time.Hour)}, // past!
		LastBeacon:      store.LooseTime{Time: now.Add(-time.Minute)},
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: 30 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()

	if err := sch.ReviveNow(ctx, "A"); err != nil {
		t.Fatalf("ReviveNow: %v", err)
	}
	// Give one tick to fire the live beacon.
	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done

	if r.KilledCount() != 0 {
		t.Errorf("revive should not send kill packets, got %d", r.KilledCount())
	}
	if r.LiveCount() < 1 {
		t.Errorf("revive should fire a live beacon on next tick, got %d", r.LiveCount())
	}
	got, _ := st.Get("A")
	if got.IsKilled() {
		t.Error("object should be live after revive")
	}
	if !got.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt should be cleared (else past expiry re-kills immediately): %v", got.ExpiresAt)
	}
}

func TestReviveNow_NotKilled(t *testing.T) {
	st := newStore(t, store.Object{
		ObjectName: "A", SymbolTable: "/", SymbolID: "r",
		Enabled: true, IntervalMinutes: 30,
	})
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { sch.Run(ctx); close(done) }()
	if err := sch.ReviveNow(ctx, "A"); !errors.Is(err, store.ErrNotKilled) {
		t.Errorf("got %v, want ErrNotKilled", err)
	}
	cancel()
	<-done
}

func TestBeaconNow_Unknown(t *testing.T) {
	st := newStore(t)
	r := &recorder{}
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: time.Hour})
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
	sch := New(st, r.Live(), r.Killed(), Options{TickInterval: 30 * time.Millisecond})
	runFor(t, sch, 100*time.Millisecond)

	if got, _ := st.Get("A"); !got.LastBeacon.IsZero() {
		t.Errorf("LastBeacon updated despite failure: %v", got.LastBeacon)
	}
}
