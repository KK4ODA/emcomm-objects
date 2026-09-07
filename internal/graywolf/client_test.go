package graywolf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"github.com/kk4oda/emcomm-objects/internal/link"
)

// fakeGraywolf mimics the subset of Graywolf's API we use: cookie login,
// version, beacons CRUD + send.
type fakeGraywolf struct {
	mu      sync.Mutex
	nextID  int
	beacons map[int]Beacon
	sent    []int
	logins  int
	user    string
	pass    string
	srv     *httptest.Server
}

func newFakeGraywolf(t *testing.T) *fakeGraywolf {
	f := &fakeGraywolf{nextID: 1, beacons: map[int]Beacon{}, user: "op", pass: "secret"}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/version", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Version{Version: "0.14.13", Commit: "abc", Platform: "windows"})
	})
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.logins++
		f.mu.Unlock()
		if body["username"] != f.user || body["password"] != f.pass {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid credentials"}`))
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/"})
		w.Write([]byte(`{"ok":true}`))
	})
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if c, err := r.Cookie("session"); err != nil || c.Value != "ok" {
				w.WriteHeader(401)
				w.Write([]byte(`{"error":"authentication required"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("GET /api/beacons", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		out := []Beacon{}
		for _, b := range f.beacons {
			out = append(out, b)
		}
		json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("POST /api/beacons", auth(func(w http.ResponseWriter, r *http.Request) {
		var b Beacon
		json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		b.ID = f.nextID
		f.nextID++
		f.beacons[b.ID] = b
		f.mu.Unlock()
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(b)
	}))
	mux.HandleFunc("PUT /api/beacons/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		id := atoi(r.PathValue("id"))
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.beacons[id]; !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"not found"}`))
			return
		}
		var b Beacon
		json.NewDecoder(r.Body).Decode(&b)
		b.ID = id
		f.beacons[id] = b
		json.NewEncoder(w).Encode(b)
	}))
	mux.HandleFunc("DELETE /api/beacons/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		id := atoi(r.PathValue("id"))
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.beacons[id]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(f.beacons, id)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /api/beacons/{id}/send", auth(func(w http.ResponseWriter, r *http.Request) {
		id := atoi(r.PathValue("id"))
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.beacons[id]; !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"not found"}`))
			return
		}
		f.sent = append(f.sent, id)
		w.Write([]byte(`{"status":"sent"}`))
	}))
	mux.HandleFunc("GET /api/channels", auth(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"id":1,"name":"VHF","mode":"aprs","modem_type":"afsk1200"}]`))
	}))
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func packet(killed bool) link.Packet {
	src, _ := ax25.ParseAddress("KK4ODA-12")
	dst, _ := ax25.ParseAddress("APZEMC")
	path, _ := ax25.ParsePath("WIDE1-1")
	info := ";SHELTER1 *062000z3348.00N/08418.00WhRed Cross"
	if killed {
		info = ";SHELTER1 _062000z3348.00N/08418.00WhRed Cross"
	}
	return link.Packet{Object: "SHELTER1", Killed: killed, Source: src, Dest: dst, Path: path, Info: info,
		Latitude: 33.8, Longitude: -84.3, SymbolTab: '/', SymbolCode: 'h', Comment: "Red Cross", IntervalSeconds: 900}
}

func TestClientTestAndChannels(t *testing.T) {
	f := newFakeGraywolf(t)
	c := NewClient(f.srv.URL, "op", "secret")
	r := c.Test(context.Background())
	if !r.OK || r.Version != "0.14.13" || len(r.Channels) != 1 || !r.LoggedIn {
		t.Fatalf("test result: %+v", r)
	}
	bad := NewClient(f.srv.URL, "op", "wrong").Test(context.Background())
	if bad.OK || !strings.Contains(bad.Error, "rejected") {
		t.Fatalf("bad creds accepted: %+v", bad)
	}
	none := NewClient(f.srv.URL, "", "").Test(context.Background())
	if none.OK || !strings.Contains(none.Error, "not configured") {
		t.Fatalf("missing creds: %+v", none)
	}
}

func TestTransportLiveKillRetire(t *testing.T) {
	f := newFakeGraywolf(t)
	c := NewClient(f.srv.URL, "op", "secret")
	var states []link.State
	tr := NewTransport(c, Options{Channel: 1, SendPath: "rf"}, nil, func(s link.State) { states = append(states, s) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Run(ctx)

	if err := tr.Send(ctx, packet(false)); err != link.ErrNotConnected {
		t.Fatalf("disabled transport must refuse: %v", err)
	}
	tr.SetEnabled(true)
	deadline := time.Now().Add(3 * time.Second)
	for tr.State().Status != link.Connected && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if st := tr.State(); st.Status != link.Connected || !strings.Contains(st.Detail, "0.14.13") {
		t.Fatalf("not connected: %+v", st)
	}

	// Live: creates an object beacon and sends it.
	if err := tr.Send(ctx, packet(false)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.beacons) != 1 || len(f.sent) != 1 {
		t.Fatalf("beacons=%d sent=%d", len(f.beacons), len(f.sent))
	}
	var b Beacon
	for _, v := range f.beacons {
		b = v
	}
	f.mu.Unlock()
	if b.Type != "object" || b.ObjectName != "SHELTER1" || b.Symbol != "h" || b.SymbolTable != "/" || b.Enabled || b.SlotSeconds != -1 || b.Path != "WIDE1-1" || b.Channel != 1 || b.Callsign != "KK4ODA-12" || b.Interval != 900 {
		t.Fatalf("object beacon: %+v", b)
	}

	// Second live send reuses the same beacon (update, not create).
	if err := tr.Send(ctx, packet(false)); err != nil {
		t.Fatal(err)
	}
	// Kill: same beacon becomes custom with our exact info field.
	if err := tr.Send(ctx, packet(true)); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.beacons) != 1 || len(f.sent) != 3 {
		t.Fatalf("beacons=%d sent=%d", len(f.beacons), len(f.sent))
	}
	for _, v := range f.beacons {
		b = v
	}
	f.mu.Unlock()
	if b.Type != "custom" || !strings.HasPrefix(b.CustomInfo, ";SHELTER1 _") || b.Comment != "" {
		t.Fatalf("kill beacon: %+v", b)
	}

	// Retire deletes it.
	if err := tr.Retire(ctx, "SHELTER1"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	n := len(f.beacons)
	f.mu.Unlock()
	if n != 0 {
		t.Fatalf("beacon not deleted")
	}
	// Retiring an unknown object is fine.
	if err := tr.Retire(ctx, "NOPE"); err != nil {
		t.Fatal(err)
	}
}

func TestTransportRecoversDeletedBeacon(t *testing.T) {
	f := newFakeGraywolf(t)
	c := NewClient(f.srv.URL, "op", "secret")
	tr := NewTransport(c, Options{SendPath: "both"}, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tr.Run(ctx)
	tr.SetEnabled(true)
	for i := 0; i < 300 && tr.State().Status != link.Connected; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if err := tr.Send(ctx, packet(false)); err != nil {
		t.Fatal(err)
	}
	// Someone deletes it in Graywolf's UI.
	f.mu.Lock()
	f.beacons = map[int]Beacon{}
	f.mu.Unlock()
	if err := tr.Send(ctx, packet(false)); err != nil {
		t.Fatalf("should recreate: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.beacons) != 1 || len(f.sent) != 2 {
		t.Fatalf("beacons=%d sent=%d", len(f.beacons), len(f.sent))
	}
}

func TestSplitSymbol(t *testing.T) {
	tbl, sym, ov := splitSymbol('/', 'h')
	if tbl != "/" || sym != "h" || ov != "" {
		t.Fatal("primary")
	}
	tbl, sym, ov = splitSymbol('S', '#')
	if tbl != `\` || sym != "#" || ov != "S" {
		t.Fatalf("overlay: %s %s %s", tbl, sym, ov)
	}
}

func TestSessionExpiryRelogin(t *testing.T) {
	f := newFakeGraywolf(t)
	c := NewClient(f.srv.URL, "op", "secret")
	if _, err := c.ListBeacons(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Drop the cookie jar: next call gets 401 and must re-login transparently.
	c.Reconfigure(f.srv.URL, "op", "secret")
	c.http.Jar, _ = newJar()
	if _, err := c.ListBeacons(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logins < 2 {
		t.Fatalf("expected re-login, logins=%d", f.logins)
	}
}
