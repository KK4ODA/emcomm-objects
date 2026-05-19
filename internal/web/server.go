// Package web serves the local browser UI for managing and beaconing APRS
// objects. The UI is a single embedded HTML page with vanilla JS + Leaflet
// (loaded from CDN). All state lives server-side; the UI is a thin view.
package web

import (
	"context"
	"embed"
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
	"github.com/kk4oda/emcomm-objects/internal/kiss"
	"github.com/kk4oda/emcomm-objects/internal/scheduler"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

//go:embed ui
var uiFS embed.FS

// Server is the local web UI HTTP server.
type Server struct {
	cfg    config.Config
	store  *store.Store
	sched  *scheduler.Scheduler
	kiss   *kiss.Client
	broker *Broker
	log    *slog.Logger
}

// New constructs a Server. The caller is responsible for the lifecycle of
// the underlying store/scheduler/kiss client.
func New(cfg config.Config, st *store.Store, sch *scheduler.Scheduler, kc *kiss.Client, broker *Broker, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{cfg: cfg, store: st, sched: sch, kiss: kc, broker: broker, log: log}
}

// ListenAndServe starts the HTTP server. Returns when ctx is cancelled or
// the listener fails.
func (s *Server) ListenAndServe(ctx context.Context) error {
	mux := s.routes()
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	ln, err := net.Listen("tcp", s.cfg.Web.Listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Web.Listen, err)
	}
	s.log.Info("web ui listening", "url", "http://"+ln.Addr().String()+"/")
	// Shut down when context cancels.
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

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()

	// Static UI assets, stripped of the `ui/` prefix.
	uiSub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		// Compile-time guarantee: ui/ exists in the embed.
		panic(err)
	}
	mux.Handle("GET /", http.FileServer(http.FS(uiSub)))

	mux.HandleFunc("GET /api/objects", s.handleListObjects)
	mux.HandleFunc("GET /api/objects/{name}", s.handleGetObject)
	mux.HandleFunc("PUT /api/objects/{name}", s.handleUpsertObject)
	mux.HandleFunc("DELETE /api/objects/{name}", s.handleDeleteObject)
	mux.HandleFunc("POST /api/objects/{name}/beacon", s.handleBeaconNow)
	mux.HandleFunc("POST /api/objects/{name}/kill", s.handleKillNow)
	mux.HandleFunc("POST /api/objects/{name}/revive", s.handleReviveNow)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("GET /api/events", s.handleSSE)

	return mux
}

// --- handlers ---

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.store.List())
}

func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	o, ok := s.store.Get(name)
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
	// Enforce URL/body name consistency.
	if body.ObjectName == "" {
		body.ObjectName = name
	}
	if body.ObjectName != name {
		writeError(w, http.StatusBadRequest, "URL name %q does not match body ObjectName %q", name, body.ObjectName)
		return
	}
	if err := s.store.Upsert(body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid object: %v", err)
		return
	}
	if err := s.store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "save: %v", err)
		return
	}
	out, _ := s.store.Get(name)
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleDeleteObject(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.store.Delete(name) {
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	}
	if err := s.store.Save(); err != nil {
		writeError(w, http.StatusInternalServerError, "save: %v", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleBeaconNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.sched.BeaconNow(ctx, name); err != nil {
		if errors.Is(err, scheduler.ErrObjectNotFound) {
			writeError(w, http.StatusNotFound, "object %q not found", name)
			return
		}
		if errors.Is(err, scheduler.ErrObjectKilled) {
			writeError(w, http.StatusConflict, "cannot beacon: object %q is killed", name)
			return
		}
		writeError(w, http.StatusInternalServerError, "beacon: %v", err)
		return
	}
	// Return the updated object (LastBeacon is now set).
	if o, ok := s.store.Get(name); ok {
		writeJSON(w, http.StatusOK, o)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleKillNow transitions the object to killed status and fires the first
// kill packet immediately. Drains 2 more kill packets over the next ticks.
//
// Status responses:
//
//	200 OK              — kill sequence started, first packet sent
//	200 OK + body note  — object was never beaconed; marked killed silently (no packets sent)
//	404 Not Found       — unknown object
//	409 Conflict        — already killed (idempotent rejection)
//	500                 — transmit failure (state still updated to killed)
func (s *Server) handleKillNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := s.sched.KillNow(ctx, name)
	switch {
	case err == nil:
		// Success: first kill sent, remainder will drain on schedule.
	case errors.Is(err, scheduler.ErrObjectNotFound):
		writeError(w, http.StatusNotFound, "object %q not found", name)
		return
	case errors.Is(err, store.ErrAlreadyKilled):
		writeError(w, http.StatusConflict, "object %q is already killed", name)
		return
	case errors.Is(err, store.ErrNeverBeaconed):
		// Not really an error from the user's perspective — we did mark it
		// killed locally, just didn't broadcast. Surface as 200 with a note
		// so the UI can display it.
		o, _ := s.store.Get(name)
		writeJSON(w, http.StatusOK, killResponse{
			Object: o,
			Note:   "object was never live-beaconed; marked killed locally without sending kill packets",
		})
		return
	default:
		writeError(w, http.StatusInternalServerError, "kill: %v", err)
		return
	}
	o, _ := s.store.Get(name)
	writeJSON(w, http.StatusOK, killResponse{Object: o})
}

type killResponse struct {
	Object store.Object `json:"object"`
	Note   string       `json:"note,omitempty"`
}

// handleReviveNow flips a killed object back to live status, clears its
// ExpiresAt, and zeroes LastBeacon so the next scheduler tick fires a
// live beacon. The object is back on the air (from receivers' perspective)
// within ~10 seconds (one tick).
//
// Status responses:
//
//	200 OK         — revived; live beacon fires on next tick
//	404 Not Found  — unknown object
//	409 Conflict   — object is not killed (revive is a state transition only)
func (s *Server) handleReviveNow(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	err := s.sched.ReviveNow(ctx, name)
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
	o, _ := s.store.Get(name)
	writeJSON(w, http.StatusOK, o)
}

type statusResponse struct {
	KISSState     string  `json:"kiss_state"`
	KISSAddress   string  `json:"kiss_address"`
	StationCall   string  `json:"station_callsign"`
	StationTocall string  `json:"station_tocall"`
	StationPath   string  `json:"station_path"`
	Recent        []Event `json:"recent"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, statusResponse{
		KISSState:     s.kiss.State().String(),
		KISSAddress:   s.cfg.KISS.Address,
		StationCall:   s.cfg.Station.Callsign,
		StationTocall: s.cfg.Station.Tocall,
		StationPath:   s.cfg.Station.Path,
		Recent:        s.broker.RecentEvents(),
	})
}

type configResponse struct {
	StationCallsign string `json:"station_callsign"`
	StationTocall   string `json:"station_tocall"`
	StationPath     string `json:"station_path"`
	KISSAddress     string `json:"kiss_address"`
	KISSPort        int    `json:"kiss_port"`
	ObjectsFile     string `json:"objects_file"`
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, configResponse{
		StationCallsign: s.cfg.Station.Callsign,
		StationTocall:   s.cfg.Station.Tocall,
		StationPath:     s.cfg.Station.Path,
		KISSAddress:     s.cfg.KISS.Address,
		KISSPort:        s.cfg.KISS.Port,
		ObjectsFile:     s.cfg.Storage.ObjectsFile,
	})
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
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if present

	ch := s.broker.Subscribe()
	defer s.broker.Unsubscribe(ch)

	// Emit a comment line to flush headers and open the stream eagerly.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			// SSE comment line keeps the connection alive through proxies.
			fmt.Fprintf(w, ": ping\n\n")
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
