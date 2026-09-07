package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/scheduler"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

type fakeLink struct{ sent []link.Packet }

func (f *fakeLink) Name() string { return "fake" }
func (f *fakeLink) Send(ctx context.Context, p link.Packet) error {
	f.sent = append(f.sent, p)
	return nil
}
func (f *fakeLink) Retire(ctx context.Context, o string) error { return nil }
func (f *fakeLink) State() link.State {
	return link.State{Transport: "fake"}.WithStatus(link.Connected)
}

func newTestServer(t *testing.T) (*httptest.Server, *store.Store, *config.Config) {
	t.Helper()
	st := store.New(filepath.Join(t.TempDir(), "objects.json"))
	cfg := config.Default()
	cfg.Station.Callsign = "KK4ODA-12"
	sent := 0
	tx := func(o store.Object, now time.Time) error { sent++; return nil }
	sch := scheduler.New(st, tx, tx, scheduler.Options{TickInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go sch.Run(ctx)
	broker := NewBroker(10)
	srv := New(Deps{
		Store: st, Scheduler: sch, Link: &fakeLink{}, Broker: broker,
		Config:      func() config.Config { return cfg },
		ApplyConfig: func(c config.Config) error { cfg = c; return nil },
		DataDir:     t.TempDir(), ConfigPath: "config.yaml", Mode: "source",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, st, &cfg
}

func do(t *testing.T, method, url string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rdr = bytes.NewReader(data)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, _ := http.NewRequest(method, url, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestObjectLifecycleOverHTTP(t *testing.T) {
	ts, st, _ := newTestServer(t)
	obj := map[string]any{"ObjectName": "SHELTER1", "SymbolTable": "/", "SymbolID": "h", "Latitude": 33.8, "Longitude": -84.3,
		"Comment": "Red Cross", "IntervalMinutes": 15, "Enabled": true, "ShowTooltip": true, "LastBeacon": "0001-01-01T00:00:00"}
	resp, body := do(t, http.MethodPut, ts.URL+"/api/objects/SHELTER1", obj)
	if resp.StatusCode != 200 {
		t.Fatalf("upsert: %d %s", resp.StatusCode, body)
	}
	if resp, body = do(t, http.MethodPut, ts.URL+"/api/objects/OTHER", obj); resp.StatusCode != 400 {
		t.Fatalf("name mismatch accepted: %d %s", resp.StatusCode, body)
	}
	resp, _ = do(t, http.MethodPost, ts.URL+"/api/objects/SHELTER1/beacon", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("beacon: %d", resp.StatusCode)
	}
	if o, _ := st.Get("SHELTER1"); o.LastBeacon.IsZero() {
		t.Fatal("LastBeacon not set")
	}
	resp, body = do(t, http.MethodPost, ts.URL+"/api/objects/SHELTER1/kill", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"killed"`) {
		t.Fatalf("kill: %d %s", resp.StatusCode, body)
	}
	if resp, _ = do(t, http.MethodPost, ts.URL+"/api/objects/SHELTER1/kill", nil); resp.StatusCode != 409 {
		t.Fatalf("second kill: %d", resp.StatusCode)
	}
	if resp, _ = do(t, http.MethodPost, ts.URL+"/api/objects/SHELTER1/beacon", nil); resp.StatusCode != 409 {
		t.Fatalf("beacon while killed: %d", resp.StatusCode)
	}
	if resp, _ = do(t, http.MethodPost, ts.URL+"/api/objects/SHELTER1/revive", nil); resp.StatusCode != 200 {
		t.Fatalf("revive: %d", resp.StatusCode)
	}
	resp, body = do(t, http.MethodGet, ts.URL+"/api/objects.csv", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "SHELTER1,33.800000,-84.300000,/,h,Red Cross,15,true") {
		t.Fatalf("csv: %d %s", resp.StatusCode, body)
	}
	if resp, _ = do(t, http.MethodDelete, ts.URL+"/api/objects/SHELTER1", nil); resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ = do(t, http.MethodGet, ts.URL+"/api/objects/SHELTER1", nil); resp.StatusCode != 404 {
		t.Fatalf("after delete: %d", resp.StatusCode)
	}
}

func TestConfigRoundTripHidesPassword(t *testing.T) {
	ts, _, cfg := newTestServer(t)
	cfg.Graywolf.Password = "secret"
	resp, body := do(t, http.MethodGet, ts.URL+"/api/config", nil)
	if resp.StatusCode != 200 || strings.Contains(string(body), "secret") || !strings.Contains(string(body), `"password_set": true`) {
		t.Fatalf("config leaked or flag missing: %s", body)
	}
	var v configView
	json.Unmarshal(body, &v)
	v.Station.Callsign = "n0call-1"
	v.Transport = "kiss"
	v.Graywolf.Password = "" // keep
	resp, body = do(t, http.MethodPut, ts.URL+"/api/config", v)
	if resp.StatusCode != 200 {
		t.Fatalf("put config: %d %s", resp.StatusCode, body)
	}
	if cfg.Station.Callsign != "N0CALL-1" || cfg.Transport != "kiss" || cfg.Graywolf.Password != "secret" {
		t.Fatalf("applied config: %+v", cfg)
	}
	v.Station.Callsign = "BAD-99"
	if resp, _ = do(t, http.MethodPut, ts.URL+"/api/config", v); resp.StatusCode != 400 {
		t.Fatalf("invalid config accepted: %d", resp.StatusCode)
	}
	resp, body = do(t, http.MethodGet, ts.URL+"/api/status", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `"callsign": "N0CALL-1"`) {
		t.Fatalf("status: %s", body)
	}
}

func TestStaticUIServed(t *testing.T) {
	ts, _, _ := newTestServer(t)
	resp, body := do(t, http.MethodGet, ts.URL+"/", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<title>") {
		t.Fatalf("index: %d", resp.StatusCode)
	}
	if resp, _ = do(t, http.MethodGet, ts.URL+"/vendor/leaflet.js", nil); resp.StatusCode != 200 {
		t.Fatalf("leaflet not embedded: %d", resp.StatusCode)
	}
}
