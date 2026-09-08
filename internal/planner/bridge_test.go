package planner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/kk4oda/emcomm-objects/internal/config"
)

type fakeGW struct {
	mu        sync.Mutex
	stations  []map[string]any
	sent      []string
	fail      bool
	sinceSeen []string
}

func (f *fakeGW) ListStations(_ context.Context, _ int, since string) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sinceSeen = append(f.sinceSeen, since)
	return f.stations, nil
}
func (f *fakeGW) SendMessage(_ context.Context, to, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("radio busy")
	}
	f.sent = append(f.sent, to+": "+text)
	return nil
}
func (f *fakeGW) StationCallsign(context.Context) (string, error) { return "N0CALL-1", nil }

type plannerStub struct {
	mu       sync.Mutex
	token    string
	received [][]map[string]any
	acks     []map[string]any
	outbox   []map[string]any
	calls    []string
}

func (p *plannerStub) handler() http.Handler {
	mux := http.NewServeMux()
	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			p.calls = append(p.calls, r.Method+" "+r.URL.Path)
			p.mu.Unlock()
			if r.Header.Get("Authorization") != "Bearer "+p.token {
				w.WriteHeader(401)
				_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("/aprs-ingest/ping", auth(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "bridge": "EOC"})
	}))
	mux.HandleFunc("/aprs-ingest/stations", auth(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stations []map[string]any `json:"stations"`
		}
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		p.mu.Lock()
		p.received = append(p.received, body.Stations)
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"received": len(body.Stations), "stored": len(body.Stations)})
	}))
	mux.HandleFunc("/aprs-ingest/outbox", auth(func(w http.ResponseWriter, _ *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": p.outbox})
	}))
	mux.HandleFunc("/aprs-ingest/outbox/ack", auth(func(w http.ResponseWriter, r *http.Request) {
		var ack map[string]any
		_ = json.NewDecoder(r.Body).Decode(&ack)
		p.mu.Lock()
		p.acks = append(p.acks, ack)
		p.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	return mux
}

func newBridge(t *testing.T, gw *fakeGW, srv *httptest.Server, token string) (*Bridge, config.Planner) {
	t.Helper()
	p := config.Planner{Enabled: true, URL: srv.URL, Token: token, ForwardStations: true, SendMessages: true, IntervalSeconds: 30, LookbackSeconds: 600}
	b := New(gw, func() config.Config { c := config.Default(); c.Planner = p; return c }, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	return b, p
}

func TestCycleForwardsStationsAndAdvancesCursor(t *testing.T) {
	stub := &plannerStub{token: "ebt_test"}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	gw := &fakeGW{stations: []map[string]any{
		{"callsign": "KK4ODA-9", "last_heard": "2026-03-07T10:00:00Z", "positions": []any{map[string]any{"lat": 33.8, "lon": -84.1}}},
		{"callsign": "W4CEF", "last_heard": "2026-03-07T10:05:00Z"},
	}}
	b, p := newBridge(t, gw, srv, "ebt_test")
	b.cycle(context.Background(), p)
	if len(stub.received) != 1 || len(stub.received[0]) != 2 {
		t.Fatalf("planner did not receive both stations: %+v", stub.received)
	}
	s := b.State()
	if !s.Connected || s.StationsSent != 2 || s.LastError != "" {
		t.Fatalf("state = %+v", s)
	}
	b.cycle(context.Background(), p)
	if got := gw.sinceSeen[1]; got != "2026-03-07T10:05:00Z" {
		t.Fatalf("second cycle should ask since the newest last_heard, got %q", got)
	}
}

func TestOutboxIsSentOnceAndAcked(t *testing.T) {
	stub := &plannerStub{token: "ebt_test", outbox: []map[string]any{{"id": "m1", "to_callsign": "KK4ODA-7", "text": "Plan updated: PAM (v3)"}}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	gw := &fakeGW{}
	b, p := newBridge(t, gw, srv, "ebt_test")
	b.cycle(context.Background(), p)
	b.cycle(context.Background(), p) // the stub keeps returning m1 as pending; the bridge must not send it twice
	if len(gw.sent) != 1 || gw.sent[0] != "KK4ODA-7: Plan updated: PAM (v3)" {
		t.Fatalf("sent = %v", gw.sent)
	}
	if len(stub.acks) != 1 || stub.acks[0]["ok"] != true || stub.acks[0]["id"] != "m1" {
		t.Fatalf("acks = %v", stub.acks)
	}
	if b.State().MessagesSent != 1 {
		t.Fatalf("state = %+v", b.State())
	}
}

func TestOutboxFailureIsAckedAsFailed(t *testing.T) {
	stub := &plannerStub{token: "ebt_test", outbox: []map[string]any{{"id": "m2", "to_callsign": "W4CEF-9", "text": "hi"}}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	gw := &fakeGW{fail: true}
	b, p := newBridge(t, gw, srv, "ebt_test")
	b.cycle(context.Background(), p)
	if len(stub.acks) != 1 || stub.acks[0]["ok"] != false || stub.acks[0]["error"] != "radio busy" {
		t.Fatalf("acks = %v", stub.acks)
	}
	if b.State().MessagesFailed != 1 {
		t.Fatalf("state = %+v", b.State())
	}
}

func TestBadTokenIsReportedClearly(t *testing.T) {
	stub := &plannerStub{token: "ebt_right"}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()
	b, p := newBridge(t, &fakeGW{stations: []map[string]any{{"callsign": "X"}}}, srv, "ebt_wrong")
	b.cycle(context.Background(), p)
	s := b.State()
	if s.Connected || s.LastError == "" || !contains(s.LastError, "rejected the token") {
		t.Fatalf("state = %+v", s)
	}
	if _, err := b.Test(context.Background(), p); err == nil {
		t.Fatal("Test should fail with a bad token")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
