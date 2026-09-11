package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDefaultsAndValidate(t *testing.T) {
	p := writeTemp(t, "station:\n  callsign: kk4oda-12\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Station.Callsign != "KK4ODA-12" {
		t.Fatalf("callsign not upper-cased: %q", cfg.Station.Callsign)
	}
	if cfg.Transport != TransportGraywolf || cfg.Graywolf.URL != "http://127.0.0.1:8080" {
		t.Fatalf("defaults missing: %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestLoadEmptyFileIsDefaults(t *testing.T) {
	cfg, err := Load(writeTemp(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Web.Listen != "127.0.0.1:8765" {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}
	if !cfg.SetupNeeded() {
		t.Fatal("default config must need setup")
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	p := writeTemp(t, "station:\n  callsign: X1Y\n  bogus: 1\n")
	if _, err := Load(p); err == nil {
		t.Fatal("unknown field should fail")
	}
}

func TestExampleParsesAndValidates(t *testing.T) {
	cfg, err := Load(writeTemp(t, ExampleYAML))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("example must validate: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	base := Default()
	base.Station.Callsign = "KK4ODA-12"
	cases := map[string]func(*Config){
		"bad ssid":      func(c *Config) { c.Station.Callsign = "KK4ODA-16" },
		"bad path":      func(c *Config) { c.Station.Path = "WIDE1-1,,X" },
		"bad transport": func(c *Config) { c.Transport = "carrier-pigeon" },
		"bad send_path": func(c *Config) { c.Graywolf.SendPath = "sometimes" },
		"bad gw url":    func(c *Config) { c.Graywolf.URL = "ftp://x" },
		"bad kiss":      func(c *Config) { c.Transport = TransportKISS; c.KISS.Address = "nohost" },
		"bad kiss port": func(c *Config) { c.Transport = TransportKISS; c.KISS.Port = 16 },
		"bad listen":    func(c *Config) { c.Web.Listen = "127.0.0.1:99999" },
	}
	for name, mut := range cases {
		c := base
		mut(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSaveRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.yaml")
	c := Default()
	c.Station.Callsign = "kk4oda-12"
	c.Graywolf.Password = "hunter2"
	c.Transport = TransportKISS
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	back, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.Station.Callsign != "KK4ODA-12" || back.Graywolf.Password != "hunter2" || back.Transport != TransportKISS {
		t.Fatalf("round trip lost data: %+v", back)
	}
	data, _ := os.ReadFile(p)
	if !strings.HasPrefix(string(data), "# emcomm-objects configuration") {
		t.Fatal("header missing")
	}
}

func TestNormalizePlannerURL(t *testing.T) {
	cases := map[string]string{
		"https://abc.supabase.co/functions/v1":                                    "https://abc.supabase.co/functions/v1",
		"https://abc.supabase.co/functions/v1/":                                   "https://abc.supabase.co/functions/v1",
		"https://abc.supabase.co/functions/v1/aprs-ingest/action?token=ebt_x":     "https://abc.supabase.co/functions/v1",
		"https://abc.supabase.co/functions/v1/aprs-ingest":                        "https://abc.supabase.co/functions/v1",
		"abc.supabase.co/functions/v1":                                            "https://abc.supabase.co/functions/v1",
		"https://emcommplanner.org":                                               defaultPlannerURL,
		"https://emcommplanner.org/aprs":                                          defaultPlannerURL,
		"":                                                                        "",
	}
	for in, want := range cases {
		if got := NormalizePlannerURL(in); got != want {
			t.Errorf("NormalizePlannerURL(%q) = %q, want %q", in, got, want)
		}
	}
}
