package transmit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

type fakeLink struct {
	name    string
	enabled bool
	sent    []link.Packet
	retired []string
	fail    error
}

func (f *fakeLink) Name() string { return f.name }
func (f *fakeLink) Send(ctx context.Context, p link.Packet) error {
	if f.fail != nil {
		return f.fail
	}
	f.sent = append(f.sent, p)
	return nil
}
func (f *fakeLink) Retire(ctx context.Context, o string) error {
	f.retired = append(f.retired, o)
	return nil
}
func (f *fakeLink) State() link.State {
	return link.State{Transport: f.name}.WithStatus(link.Connected)
}
func (f *fakeLink) SetEnabled(on bool) { f.enabled = on }

func testCfg() config.Config {
	c := config.Default()
	c.Station.Callsign = "KK4ODA-12"
	c.Station.Path = "WIDE1-1"
	return c
}

func TestSenderBuildsLiveAndKill(t *testing.T) {
	fl := &fakeLink{name: "graywolf"}
	s := NewSender(testCfg(), fl, nil)
	var events []PacketEvent
	s.OnPacket = func(e PacketEvent) { events = append(events, e) }
	o := store.Object{ObjectName: "SHELTER1", SymbolTable: "/", SymbolID: "h", Latitude: 33.8, Longitude: -84.3, Comment: "Red Cross", IntervalMinutes: 15, Path: "-"}
	now := time.Date(2026, 9, 6, 20, 0, 0, 0, time.UTC)
	if err := s.Transmit(o, now); err != nil {
		t.Fatal(err)
	}
	if err := s.TransmitKilled(o, now); err != nil {
		t.Fatal(err)
	}
	if len(fl.sent) != 2 {
		t.Fatalf("sent %d packets", len(fl.sent))
	}
	live, kill := fl.sent[0], fl.sent[1]
	if !strings.HasPrefix(live.Info, ";SHELTER1 *062000z3348.00N/08418.00WhRed Cross") {
		t.Fatalf("live info %q", live.Info)
	}
	if !strings.HasPrefix(kill.Info, ";SHELTER1 _062000z") || !kill.Killed {
		t.Fatalf("kill info %q", kill.Info)
	}
	if live.Path != nil {
		t.Fatalf("per-object direct override ignored: %v", live.Path)
	}
	if live.IntervalSeconds != 900 || live.Source.String() != "KK4ODA-12" || live.Dest.String() != "APZEMC" {
		t.Fatalf("packet fields: %+v", live)
	}
	if len(events) != 2 || events[0].Transport != "graywolf" {
		t.Fatalf("events: %+v", events)
	}
}

func TestSenderRefusesWithoutCallsign(t *testing.T) {
	s := NewSender(config.Default(), &fakeLink{name: "kiss"}, nil)
	o := store.Object{ObjectName: "X", SymbolTable: "/", SymbolID: "r"}
	if err := s.Transmit(o, time.Now()); err == nil {
		t.Fatal("expected error with no callsign")
	}
}

func TestRouterSwitches(t *testing.T) {
	gw := &fakeLink{name: "graywolf"}
	k := &fakeLink{name: "kiss"}
	r := NewRouter(gw, k)
	if err := r.Send(context.Background(), link.Packet{}); !errors.Is(err, link.ErrNotConnected) {
		t.Fatalf("router without active link must refuse: %v", err)
	}
	if err := r.Use("kiss"); err != nil {
		t.Fatal(err)
	}
	if !k.enabled || gw.enabled {
		t.Fatal("enable flags not applied")
	}
	_ = r.Send(context.Background(), link.Packet{Object: "A"})
	_ = r.Retire(context.Background(), "A")
	if len(k.sent) != 1 || len(k.retired) != 1 || len(gw.sent) != 0 {
		t.Fatal("packet not routed to kiss")
	}
	if r.State().Transport != "kiss" || r.Name() != "kiss" {
		t.Fatal("state not from active link")
	}
	if err := r.Use("bogus"); err == nil {
		t.Fatal("unknown transport accepted")
	}
}
