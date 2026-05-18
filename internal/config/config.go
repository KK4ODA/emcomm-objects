// Package config loads emcomm-objects's YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"gopkg.in/yaml.v3"
)

// Config is the top-level YAML structure.
type Config struct {
	Station Station `yaml:"station"`
	KISS    KISS    `yaml:"kiss"`
	Storage Storage `yaml:"storage"`
	Web     Web     `yaml:"web"`
}

// Station describes how outgoing AX.25 frames are addressed.
type Station struct {
	// Callsign with optional SSID, e.g. "KK4ODA-12". This is the SOURCE
	// callsign for beacons; it should differ from Graywolf's own station
	// SSID to avoid the iGate's self-echo suppression.
	Callsign string `yaml:"callsign"`
	// Tocall (AX.25 destination), e.g. "APZEMC". Identifies the originating
	// software in aprs.fi etc.
	Tocall string `yaml:"tocall"`
	// Digipeater path, comma-separated, e.g. "WIDE1-1,WIDE2-1". Empty for
	// direct (recommended for fixed-location objects in dense areas).
	Path string `yaml:"path"`
}

// KISS configures the TCP connection to Graywolf's KISS interface.
type KISS struct {
	Address string `yaml:"address"` // "host:port", e.g. "127.0.0.1:6700"
	Port    int    `yaml:"port"`    // KISS port byte (0-15), usually 0
}

// Storage holds filesystem paths.
type Storage struct {
	ObjectsFile string `yaml:"objects_file"` // path to objects JSON
}

// Web configures the local web UI.
type Web struct {
	Enabled bool   `yaml:"enabled"`
	Listen  string `yaml:"listen"` // "host:port", e.g. "127.0.0.1:8765"
}

// Default returns a Config with sensible defaults for fields not in the file.
func Default() Config {
	return Config{
		Station: Station{
			Callsign: "",
			Tocall:   "APZEMC",
			Path:     "",
		},
		KISS: KISS{
			Address: "127.0.0.1:6700",
			Port:    0,
		},
		Storage: Storage{
			ObjectsFile: "./objects.json",
		},
		Web: Web{
			Enabled: true,
			Listen:  "127.0.0.1:8765",
		},
	}
}

// Load reads and parses the YAML file at path, applying defaults for any
// missing fields. Returns an error if the file is invalid or the resulting
// config fails validation.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	// yaml.v3 zeroes any field it sees in the file; for fields not present
	// the default we set above persists. Strict parsing for typos.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks required fields and basic constraints.
func (c Config) Validate() error {
	if strings.TrimSpace(c.Station.Callsign) == "" {
		return fmt.Errorf("station.callsign is required (e.g. KK4ODA-12)")
	}
	if _, err := ax25.ParseAddress(c.Station.Callsign); err != nil {
		return fmt.Errorf("station.callsign %q: %w", c.Station.Callsign, err)
	}
	if _, err := ax25.ParseAddress(c.Station.Tocall); err != nil {
		return fmt.Errorf("station.tocall %q: %w", c.Station.Tocall, err)
	}
	if c.Station.Path != "" {
		if _, err := ax25.ParsePath(c.Station.Path); err != nil {
			return fmt.Errorf("station.path %q: %w", c.Station.Path, err)
		}
	}
	if c.KISS.Address == "" {
		return fmt.Errorf("kiss.address is required")
	}
	host, port, err := net.SplitHostPort(c.KISS.Address)
	if err != nil {
		return fmt.Errorf("kiss.address %q: %w", c.KISS.Address, err)
	}
	if host == "" {
		return fmt.Errorf("kiss.address %q has empty host", c.KISS.Address)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("kiss.address %q has invalid port", c.KISS.Address)
	}
	if c.KISS.Port < 0 || c.KISS.Port > 15 {
		return fmt.Errorf("kiss.port %d outside 0-15", c.KISS.Port)
	}
	if c.Storage.ObjectsFile == "" {
		return fmt.Errorf("storage.objects_file is required")
	}
	if c.Web.Enabled && c.Web.Listen == "" {
		return fmt.Errorf("web.listen is required when web.enabled is true")
	}
	return nil
}

// Source returns the parsed source address (must validate first).
func (c Config) Source() ax25.Address {
	a, _ := ax25.ParseAddress(c.Station.Callsign)
	return a
}

// Dest returns the parsed destination address (must validate first).
func (c Config) Dest() ax25.Address {
	a, _ := ax25.ParseAddress(c.Station.Tocall)
	return a
}

// Path returns the parsed digipeater path (may be nil).
func (c Config) Path() []ax25.Address {
	if c.Station.Path == "" {
		return nil
	}
	p, _ := ax25.ParsePath(c.Station.Path)
	return p
}

// WriteExample writes an annotated config file to path.
func WriteExample(path string) error {
	const yamlSrc = `# emcomm-objects configuration.

station:
  # Source callsign with SSID for transmitted beacons. Use an SSID distinct
  # from Graywolf's own station SSID to avoid the iGate's self-echo
  # suppression. e.g. if Graywolf is KK4ODA-1, use KK4ODA-12 here.
  callsign: "KK4ODA-12"

  # AX.25 destination ("tocall"). APZEMC is a safe experimental prefix that
  # identifies this app in aprs.fi etc. Change only if you have a registered
  # tocall.
  tocall: "APZEMC"

  # Digipeater path. Empty = direct (recommended for fixed-location objects
  # in dense areas). "WIDE1-1,WIDE2-1" is the most common path otherwise.
  path: ""

kiss:
  # TCP address of Graywolf's KISS interface. Must be a "modem" mode
  # interface, not "tnc" — tnc mode silently drops injected frames.
  address: "127.0.0.1:6700"

  # KISS port byte (0-15). Usually 0 unless Graywolf's ChannelMap is set.
  port: 0

storage:
  # Path to the objects JSON file. Compatible with Pinpoint APRS's
  # pinpointAprsObjects.json — you can copy yours here.
  objects_file: "./objects.json"

web:
  enabled: true
  listen: "127.0.0.1:8765"
`
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(yamlSrc), 0o644)
}
