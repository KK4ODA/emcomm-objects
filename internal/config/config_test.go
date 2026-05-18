package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoad_MinimalValid(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	yaml := `
station:
  callsign: KK4ODA-12
`
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	// Defaults kick in.
	if cfg.Station.Tocall != "APZEMC" {
		t.Errorf("tocall default: %q", cfg.Station.Tocall)
	}
	if cfg.KISS.Address != "127.0.0.1:6700" {
		t.Errorf("kiss default: %q", cfg.KISS.Address)
	}
	if cfg.Web.Listen != "127.0.0.1:8765" {
		t.Errorf("web default: %q", cfg.Web.Listen)
	}
}

func TestLoad_UnknownField(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("station:\n  callsign: KK4ODA-12\n  pizza: pepperoni\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "pizza") {
		t.Errorf("expected unknown-field error mentioning 'pizza', got: %v", err)
	}
}

func TestLoad_MissingCallsign(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("kiss:\n  address: 127.0.0.1:6700\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("expected error for missing callsign")
	}
}

func TestLoad_BadCallsign(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("station:\n  callsign: KK4ODA-99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("expected error for SSID 99")
	}
}

func TestLoad_BadKissPort(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte("station:\n  callsign: KK4ODA-12\nkiss:\n  port: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil {
		t.Error("expected error for kiss port 99")
	}
}

func TestWriteExample(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ex.yaml")
	if err := WriteExample(p); err != nil {
		t.Fatal(err)
	}
	// The example should fail validation (callsign is a placeholder) — but
	// it should at least PARSE as valid YAML with no unknown fields.
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "KK4ODA-12") {
		t.Errorf("example missing expected content")
	}
	// And it should load and validate (KK4ODA-12 is a valid format).
	if _, err := Load(p); err != nil {
		t.Errorf("example doesn't load cleanly: %v", err)
	}
}
