package logbuf

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestRingAndSinks(t *testing.T) {
	var out bytes.Buffer
	var seen []Entry
	h := New(slog.LevelInfo, 3, func(e Entry) { seen = append(seen, e) },
		slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug}))
	log := slog.New(h).With("comp", "test")
	log.Debug("hidden from ring")
	for i := 1; i <= 4; i++ {
		log.Info("msg", "n", i, "s", "a b")
	}
	log.WithGroup("g").Warn("grouped", "k", "v")

	if !strings.Contains(out.String(), "hidden from ring") {
		t.Fatal("debug record did not reach the sink")
	}
	rec := h.Recent()
	if len(rec) != 3 {
		t.Fatalf("ring kept %d", len(rec))
	}
	if rec[0].Message != `msg comp=test n=3 s="a b"` {
		t.Fatalf("oldest kept entry: %q", rec[0].Message)
	}
	if last := rec[2]; last.Level != "warn" || last.Message != "grouped comp=test g.k=v" {
		t.Fatalf("grouped entry: %+v", last)
	}
	if len(seen) != 5 {
		t.Fatalf("notify called %d times", len(seen))
	}
}
