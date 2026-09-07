// Package web serves the local browser UI (embedded static files) and the
// JSON API used by it and by companion tools such as VarMap.
package web

import (
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/graywolf"
	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/logbuf"
	"github.com/kk4oda/emcomm-objects/internal/scheduler"
	"github.com/kk4oda/emcomm-objects/internal/store"
	"github.com/kk4oda/emcomm-objects/internal/update"
	"github.com/kk4oda/emcomm-objects/internal/version"
)

//go:embed ui
var uiFS embed.FS

// Deps is everything the server needs from the rest of the app.
type Deps struct {
	Store     *store.Store
	Scheduler *scheduler.Scheduler
	Link      link.Link
	Broker    *Broker
	Logs      *logbuf.Handler
	Updater   *update.Updater
	Log       *slog.Logger

	// Config returns the live configuration; ApplyConfig validates, saves
	// and hot-applies a new one (wired in main).
	Config      func() config.Config
	ApplyConfig func(config.Config) error
	// Retire tells the transport an object is gone (delete).
	Retire func(name string) error
	// Quit asks the process to shut down.
	Quit func()

	DataDir    string
	ConfigPath string
	Mode       string
}

// Server is the local web UI HTTP server.
type Server struct {
	d       Deps
	started time.Time
}

// New constructs a Server.
func New(d Deps) *Server {
	if d.Log == nil {
		d.Log = slog.Default()
	}
	return &Server{d: d, started: time.Now()}
}

// ListenAndServe serves until ctx is cancelled. ready (may be nil) is
// called with the bound address once listening.
func (s *Server) ListenAndServe(ctx context.Context, listen string, ready func(addr string)) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listen, err)
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	s.d.Log.Info("web ui listening", "url", "http://"+ln.Addr().String()+"/")
	if ready != nil {
		ready(ln.Addr().String())
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Handler returns the routed HTTP handler (also used by tests).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	uiSub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err) // ui/ is embedded at compile time
	}
	static := http.FileServer(http.FS(uiSub))
	mux.Handle("GET /", noCache(static))

	mux.HandleFunc("GET /api/objects", s.handleListObjects)
	mux.HandleFunc("GET /api/objects.csv", s.handleExportCSV)
	mux.HandleFunc("POST /api/objects/beacon-all", s.handleBeaconAll)
	mux.HandleFunc("GET /api/objects/{name}", s.handleGetObject)
	mux.HandleFunc("PUT /api/objects/{name}", s.handleUpsertObject)
	mux.HandleFunc("DELETE /api/objects/{name}", s.handleDeleteObject)
	mux.HandleFunc("POST /api/objects/{name}/beacon", s.handleBeaconNow)
	mux.HandleFunc("POST /api/objects/{name}/kill", s.handleKillNow)
	mux.HandleFunc("POST /api/objects/{name}/revive", s.handleReviveNow)

	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/version", s.handleVersion)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("POST /api/graywolf/test", s.handleGraywolfTest)
	mux.HandleFunc("GET /api/update", s.handleUpdate)
	mux.HandleFunc("POST /api/update/check", s.handleUpdateCheck)
	mux.HandleFunc("POST /api/update/apply", s.handleUpdateApply)
	mux.HandleFunc("POST /api/update/skip", s.handleUpdateSkip)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("GET /api/events", s.handleSSE)
	mux.HandleFunc("POST /api/quit", s.handleQuit)
	return mux
}

// noCache keeps browsers from serving a stale UI after an update.
func noCache(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

// --- objects ---

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.d.Store.List())
}

func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	o, ok := s.d.Store.Get(name)
	if !ok {
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	}
	writeJSON(w, http.StatusOK, o)
}

func (s *Server) handleUpsertObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body store.Object
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	if body.ObjectName == "" {
		body.ObjectName = name
	}
	if body.ObjectName != name {
		writeError(w, http.StatusBadRequest, "URL name %q does not match body ObjectName %q", name, body.ObjectName)
		return
	}
	if err := s.d.Store.Upsert(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid object: %v", err)
		return
	}
	if err := s.d.Store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "save: %v", err)
		return
	}
	s.d.Scheduler.Wake()
	out, _ := s.d.Store.Get(name)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.d.Store.Delete(name) {
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	}
	if err := s.d.Store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "save: %v", err)
		return
	}
	if s.d.Retire != nil {
		if err := s.d.Retire(name); err != nil {
			s.d.Log.Warn("retire on delete failed", "object", name, "err", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="emcomm-objects.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ObjectName", "Latitude", "Longitude", "SymbolTable", "SymbolID", "Comment", "IntervalMinutes", "Enabled", "Path", "ExpiresAt", "Status"})
	for _, o := range s.d.Store.List() {
		exp := ""
		if !o.ExpiresAt.IsZero() {
			exp = o.ExpiresAt.UTC().Format(time.RFC3339)
		}
		_ = cw.Write([]string{o.ObjectName, fmt.Sprintf("%.6f", o.Latitude), fmt.Sprintf("%.6f", o.Longitude),
			o.SymbolTable, o.SymbolID, o.Comment, fmt.Sprint(o.IntervalMinutes), fmt.Sprint(o.Enabled), o.Path, exp, o.StatusOrDefault()})
	}
	cw.Flush()
}

func (s *Server) handleBeaconNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.d.Scheduler.BeaconNow(ctx, name); err != nil {
		switch {
		case errors.Is(err, scheduler.ErrObjectNotFound):
			writeError(w, http.StatusNotFound, "object %q not found", name)
		case errors.Is(err, scheduler.ErrObjectKilled):
			writeError(w, http.StatusConflict, "cannot beacon: object %q is killed", name)
		case errors.Is(err, link.ErrNotConnected):
			writeError(w, http.StatusServiceUnavailable, "transport not connected: %v", err)
		default:
			writeError(w, http.StatusBadGateway, "beacon: %v", err)
		}
		return
	}
	o, _ := s.d.Store.Get(name)
	writeJSON(w, http.StatusOK, o)
}

// handleBeaconAll fires every enabled live object now.
func (s *Server) handleBeaconAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	sent, failed := 0, []string{}
	for _, o := range s.d.Store.List() {
		if o.IsKilled() || !o.Enabled {
			continue
		}
		if err := s.d.Scheduler.BeaconNow(ctx, o.ObjectName); err != nil {
			failed = append(failed, o.ObjectName+": "+err.Error())
			continue
		}
		sent++
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent, "failed": failed})
}

type killResponse struct {
	Object store.Object `json:"object"`
	Note   string       `json:"note,omitempty"`
}

func (s *Server) handleKillNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	err := s.d.Scheduler.KillNow(ctx, name)
	switch {
	case err == nil:
	case errors.Is(err, scheduler.ErrObjectNotFound):
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	case errors.Is(err, store.ErrAlreadyKilled):
		writeError(w, http.StatusConflict, "object %q is already killed", name)
		return
	case errors.Is(err, store.ErrNeverBeaconed):
		o, _ := s.d.Store.Get(name)
		writeJSON(w, http.StatusOK, killResponse{Object: o, Note: "never beaconed: marked killed locally, nothing sent"})
		return
	case errors.Is(err, link.ErrNotConnected):
		// Already marked killed; the packets go out when the transport is back.
		o, _ := s.d.Store.Get(name)
		writeJSON(w, http.StatusOK, killResponse{Object: o, Note: "transport down: kill packets will be sent when it reconnects"})
		return
	default:
		writeError(w, http.StatusBadGateway, "kill: %v", err)
		return
	}
	o, _ := s.d.Store.Get(name)
	writeJSON(w, http.StatusOK, killResponse{Object: o})
}

func (s *Server) handleReviveNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := s.d.Scheduler.ReviveNow(ctx, name)
	switch {
	case err == nil:
	case errors.Is(err, scheduler.ErrObjectNotFound):
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	case errors.Is(err, store.ErrNotKilled):
		writeError(w, http.StatusConflict, "object %q is not killed", name)
		return
	default:
		writeError(w, http.StatusInternalServerError, "revive: %v", err)
		return
	}
	o, _ := s.d.Store.Get(name)
	writeJSON(w, http.StatusOK, o)
}

// --- status / config ---

type statusResponse struct {
	Version     string        `json:"version"`
	SetupNeeded bool          `json:"setup_needed"`
	Transport   link.State    `json:"transport"`
	Station     stationInfo   `json:"station"`
	DataDir     string        `json:"data_dir"`
	ConfigPath  string        `json:"config_path"`
	ObjectsFile string        `json:"objects_file"`
	Mode        string        `json:"install_mode"`
	KillCount   int           `json:"kill_beacon_count"`
	KillSpacing float64       `json:"kill_beacon_interval_seconds"`
	Update      *update.State `json:"update,omitempty"`
	Recent      []Event       `json:"recent"`
	Uptime      float64       `json:"uptime_seconds"`
}

type stationInfo struct {
	Callsign string `json:"callsign"`
	Tocall   string `json:"tocall"`
	Path     string `json:"path"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.d.Config()
	resp := statusResponse{
		Version:     version.Version,
		SetupNeeded: cfg.SetupNeeded(),
		Transport:   s.d.Link.State(),
		Station:     stationInfo{Callsign: cfg.Station.Callsign, Tocall: cfg.Station.Tocall, Path: cfg.Station.Path},
		DataDir:     s.d.DataDir,
		ConfigPath:  s.d.ConfigPath,
		ObjectsFile: s.d.Store.Path(),
		Mode:        s.d.Mode,
		KillCount:   s.d.Scheduler.KillBeaconCount(),
		KillSpacing: s.d.Scheduler.KillBeaconInterval().Seconds(),
		Recent:      s.d.Broker.RecentEvents(),
		Uptime:      time.Since(s.started).Seconds(),
	}
	if s.d.Updater != nil {
		u := s.d.Updater.Snapshot()
		resp.Update = &u
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version, "commit": version.Commit, "build_date": version.BuildDate})
}

// configView is the config as the Settings form sees it: the Graywolf
// password is never sent to the browser, only whether one is stored.
type configView struct {
	Station   config.Station `json:"station"`
	Transport string         `json:"transport"`
	Graywolf  struct {
		URL         string `json:"url"`
		Username    string `json:"username"`
		Password    string `json:"password"` // empty on GET; on PUT empty = keep
		PasswordSet bool   `json:"password_set"`
		Channel     int    `json:"channel"`
		SendPath    string `json:"send_path"`
	} `json:"graywolf"`
	KISS    config.KISS    `json:"kiss"`
	Storage config.Storage `json:"storage"`
	Web     config.Web     `json:"web"`
	Updates config.Updates `json:"updates"`
}

func viewOf(c config.Config) configView {
	v := configView{Station: c.Station, Transport: c.Transport, KISS: c.KISS, Storage: c.Storage, Web: c.Web, Updates: c.Updates}
	v.Graywolf.URL = c.Graywolf.URL
	v.Graywolf.Username = c.Graywolf.Username
	v.Graywolf.PasswordSet = c.Graywolf.Password != ""
	v.Graywolf.Channel = c.Graywolf.Channel
	v.Graywolf.SendPath = c.Graywolf.SendPath
	return v
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, viewOf(s.d.Config()))
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var v configView
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: %v", err)
		return
	}
	cur := s.d.Config()
	next := cur
	next.Station = v.Station
	next.Transport = v.Transport
	next.Graywolf = config.Graywolf{URL: v.Graywolf.URL, Username: v.Graywolf.Username, Password: cur.Graywolf.Password,
		Channel: v.Graywolf.Channel, SendPath: v.Graywolf.SendPath}
	if v.Graywolf.Password != "" {
		next.Graywolf.Password = v.Graywolf.Password
	}
	next.KISS = v.KISS
	next.Storage = v.Storage
	next.Web = v.Web
	next.Updates = v.Updates
	next.Normalize()
	if err := next.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	if err := s.d.ApplyConfig(next); err != nil {
		writeError(w, http.StatusInternalServerError, "apply: %v", err)
		return
	}
	var notes []string
	if cur.Web.Listen != next.Web.Listen || cur.Web.Enabled != next.Web.Enabled {
		notes = append(notes, "The web address change takes effect after a restart.")
	}
	if cur.Storage.ObjectsFile != next.Storage.ObjectsFile {
		notes = append(notes, "The objects file change takes effect after a restart.")
	}
	s.d.Broker.Notify(EventConfig)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": viewOf(next), "notes": notes})
}

// handleGraywolfTest tries the credentials from the body (falling back to
// the saved ones field by field) without saving anything.
func (s *Server) handleGraywolfTest(w http.ResponseWriter, r *http.Request) {
	cur := s.d.Config().Graywolf
	var body struct {
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	url, user, pass := cur.URL, cur.Username, cur.Password
	if u := strings.TrimSpace(body.URL); u != "" {
		url = u
		if !strings.Contains(url, "://") {
			url = "http://" + url
		}
	}
	if u := strings.TrimSpace(body.Username); u != "" {
		user = u
	}
	if body.Password != "" {
		pass = body.Password
	}
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	writeJSON(w, http.StatusOK, graywolf.NewClient(url, user, pass).Test(ctx))
}

// --- updates ---

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.d.Updater == nil {
		writeError(w, http.StatusNotFound, "updater disabled")
		return
	}
	writeJSON(w, http.StatusOK, s.d.Updater.Snapshot())
}

func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.d.Updater == nil {
		writeError(w, http.StatusNotFound, "updater disabled")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	st := s.d.Updater.Check(ctx)
	s.d.Broker.Notify(EventUpdate)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.d.Updater == nil {
		writeError(w, http.StatusNotFound, "updater disabled")
		return
	}
	// Detached from the request context: the download can take a while and
	// the process exits once the helper is launched.
	if err := s.d.Updater.Apply(context.Background()); err != nil {
		writeError(w, http.StatusBadRequest, "%v", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "Installing the update. Emcomm Objects will close and reopen in a moment."})
}

func (s *Server) handleUpdateSkip(w http.ResponseWriter, r *http.Request) {
	if s.d.Updater == nil {
		writeError(w, http.StatusNotFound, "updater disabled")
		return
	}
	var body struct {
		Version string `json:"version"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)
	if err := s.d.Updater.Skip(body.Version); err != nil {
		writeError(w, http.StatusInternalServerError, "%v", err)
		return
	}
	s.d.Broker.Notify(EventUpdate)
	writeJSON(w, http.StatusOK, s.d.Updater.Snapshot())
}

// --- logs / events / quit ---

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.d.Logs == nil {
		writeJSON(w, http.StatusOK, []logbuf.Entry{})
		return
	}
	writeJSON(w, http.StatusOK, s.d.Logs.Recent())
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := s.d.Broker.Subscribe()
	defer s.d.Broker.Unsubscribe(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data)
			flusher.Flush()
		}
	}
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	if s.d.Quit != nil {
		go func() {
			time.Sleep(300 * time.Millisecond)
			s.d.Quit()
		}()
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(body)
}

func writeError(w http.ResponseWriter, status int, format string, args ...any) {
	writeJSON(w, status, map[string]string{"error": strings.TrimSpace(fmt.Sprintf(format, args...))})
}
