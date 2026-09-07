// Package logbuf is a slog.Handler that fans records out to other handlers
// (stderr, a log file) while keeping a bounded in-memory ring and notifying
// a subscriber, so the web UI can show the server log live.
package logbuf

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Entry is one formatted log line.
type Entry struct {
	When    time.Time `json:"when"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

// Handler implements slog.Handler.
type Handler struct {
	sinks  []slog.Handler
	pre    string // attrs added with WithAttrs, already formatted with their group prefix
	groups []string
	ring   *ring
	notify func(Entry)
	level  slog.Level
}

type ring struct {
	mu   sync.Mutex
	buf  []Entry
	head int
	full bool
}

// New builds a handler keeping the last `keep` entries at or above level
// in memory. notify (may be nil) is called for every kept entry.
func New(level slog.Level, keep int, notify func(Entry), sinks ...slog.Handler) *Handler {
	if keep < 1 {
		keep = 1
	}
	if notify == nil {
		notify = func(Entry) {}
	}
	return &Handler{sinks: sinks, ring: &ring{buf: make([]Entry, keep)}, notify: notify, level: level}
}

// Enabled implements slog.Handler.
func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool {
	if l >= h.level {
		return true
	}
	for _, s := range h.sinks {
		if s.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

// Handle implements slog.Handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	for _, s := range h.sinks {
		if s.Enabled(ctx, r.Level) {
			_ = s.Handle(ctx, r.Clone())
		}
	}
	if r.Level < h.level {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(r.Message)
	sb.WriteString(h.pre)
	prefix := h.prefix()
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&sb, prefix, a)
		return true
	})
	e := Entry{When: r.Time.UTC(), Level: strings.ToLower(r.Level.String()), Message: sb.String()}
	h.ring.add(e)
	h.notify(e)
	return nil
}

func writeAttr(sb *strings.Builder, prefix string, a slog.Attr) {
	if a.Equal(slog.Attr{}) {
		return
	}
	v := a.Value.Resolve()
	if v.Kind() == slog.KindGroup {
		for _, g := range v.Group() {
			writeAttr(sb, prefix+a.Key+".", g)
		}
		return
	}
	s := v.String()
	if strings.ContainsAny(s, " \t\"") {
		s = fmt.Sprintf("%q", s)
	}
	fmt.Fprintf(sb, " %s%s=%s", prefix, a.Key, s)
}

// WithAttrs implements slog.Handler.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.sinks = make([]slog.Handler, len(h.sinks))
	for i, s := range h.sinks {
		n.sinks[i] = s.WithAttrs(attrs)
	}
	var sb strings.Builder
	sb.WriteString(h.pre)
	prefix := h.prefix()
	for _, a := range attrs {
		writeAttr(&sb, prefix, a)
	}
	n.pre = sb.String()
	return &n
}

func (h *Handler) prefix() string {
	if len(h.groups) == 0 {
		return ""
	}
	return strings.Join(h.groups, ".") + "."
}

// WithGroup implements slog.Handler.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	n := *h
	n.sinks = make([]slog.Handler, len(h.sinks))
	for i, s := range h.sinks {
		n.sinks[i] = s.WithGroup(name)
	}
	n.groups = append(append([]string{}, h.groups...), name)
	return &n
}

// Recent returns the kept entries, oldest first.
func (h *Handler) Recent() []Entry { return h.ring.snapshot() }

func (r *ring) add(e Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf[r.head] = e
	r.head = (r.head + 1) % len(r.buf)
	if r.head == 0 {
		r.full = true
	}
}

func (r *ring) snapshot() []Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		out := make([]Entry, r.head)
		copy(out, r.buf[:r.head])
		return out
	}
	out := make([]Entry, len(r.buf))
	for i := range r.buf {
		out[i] = r.buf[(r.head+i)%len(r.buf)]
	}
	return out
}
