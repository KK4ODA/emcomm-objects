// Package config loads, validates and saves emcomm-objects's YAML
// configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"gopkg.in/yaml.v3"
)

// Transport ids.
const (
	TransportGraywolf = "graywolf"
	TransportKISS     = "kiss"
)

// Config is the top-level YAML structure.
type Config struct {
	Station   Station  `yaml:"station" json:"station"`
	Transport string   `yaml:"transport" json:"transport"` // "graywolf" (default) or "kiss"
	Graywolf  Graywolf `yaml:"graywolf" json:"graywolf"`
	KISS      KISS     `yaml:"kiss" json:"kiss"`
	Storage   Storage  `yaml:"storage" json:"storage"`
	Web       Web      `yaml:"web" json:"web"`
	Updates   Updates  `yaml:"updates" json:"updates"`
}

// Station describes how outgoing frames are addressed.
type Station struct {
	// Callsign with optional SSID, e.g. "KK4ODA-12": the SOURCE of every
	// object beacon. Use an SSID distinct from Graywolf's own station SSID.
	Callsign string `yaml:"callsign" json:"callsign"`
	// Tocall (AX.25 destination), e.g. "APZEMC", identifies this software.
	Tocall string `yaml:"tocall" json:"tocall"`
	// Default digipeater path, comma-separated ("WIDE1-1"). Empty = direct.
	Path string `yaml:"path" json:"path"`
}

// Graywolf configures the REST transport (https://github.com/chrissnell/graywolf).
type Graywolf struct {
	URL      string `yaml:"url" json:"url"`           // e.g. "http://127.0.0.1:8080"
	Username string `yaml:"username" json:"username"` // Graywolf web login
	Password string `yaml:"password" json:"password"`
	// Channel is the Graywolf radio channel id to transmit on (0 = let
	// Graywolf pick its default / not needed for is_only).
	Channel int `yaml:"channel" json:"channel"`
	// SendPath: "rf" (radio only), "both" (radio + APRS-IS) or "is_only".
	SendPath string `yaml:"send_path" json:"send_path"`
}

// KISS configures the KISS-over-TCP transport.
type KISS struct {
	Address string `yaml:"address" json:"address"` // "host:port", e.g. "127.0.0.1:6700"
	Port    int    `yaml:"port" json:"port"`       // KISS port byte (0-15), usually 0
}

// Storage holds filesystem paths. Relative paths are resolved against the
// data directory (see package paths).
type Storage struct {
	ObjectsFile string `yaml:"objects_file" json:"objects_file"`
}

// Web configures the local browser UI.
type Web struct {
	Enabled     bool   `yaml:"enabled" json:"enabled"`
	Listen      string `yaml:"listen" json:"listen"`             // "host:port", e.g. "127.0.0.1:8765"
	OpenBrowser bool   `yaml:"open_browser" json:"open_browser"` // open the UI on start
}

// Updates configures the GitHub release check.
type Updates struct {
	Check         bool    `yaml:"check" json:"check"`
	IntervalHours float64 `yaml:"interval_hours" json:"interval_hours"`
	SkipVersion   string  `yaml:"skip_version" json:"skip_version"`
}

// Default returns the configuration used when a field (or the whole file)
// is absent. Graywolf's REST API is the default transport because it works
// against a stock Graywolf install with no extra Graywolf-side setup.
func Default() Config {
	return Config{
		Station:   Station{Tocall: "APZEMC", Path: "WIDE1-1"},
		Transport: TransportGraywolf,
		Graywolf:  Graywolf{URL: "http://127.0.0.1:8080", SendPath: "rf"},
		KISS:      KISS{Address: "127.0.0.1:6700"},
		Storage:   Storage{ObjectsFile: "objects.json"},
		Web:       Web{Enabled: true, Listen: "127.0.0.1:8765", OpenBrowser: true},
		Updates:   Updates{Check: true, IntervalHours: 12},
	}
}

// Load reads and parses the YAML file at path over Default(). A missing
// file yields Default() and os.ErrNotExist (wrapped) so the caller can
// start in first-run mode. A present but invalid file is a hard error.
func Load(path string) (Config, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Normalize()
	return cfg, nil
}

// Normalize trims and defaults fields so Validate and the UI see one shape.
func (c *Config) Normalize() {
	c.Station.Callsign = strings.ToUpper(strings.TrimSpace(c.Station.Callsign))
	c.Station.Tocall = strings.ToUpper(strings.TrimSpace(c.Station.Tocall))
	c.Station.Path = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(c.Station.Path), " ", ""))
	c.Transport = strings.ToLower(strings.TrimSpace(c.Transport))
	if c.Transport == "" {
		c.Transport = TransportGraywolf
	}
	c.Graywolf.URL = strings.TrimRight(strings.TrimSpace(c.Graywolf.URL), "/")
	if c.Graywolf.URL == "" {
		c.Graywolf.URL = "http://127.0.0.1:8080"
	}
	if !strings.Contains(c.Graywolf.URL, "://") {
		c.Graywolf.URL = "http://" + c.Graywolf.URL
	}
	c.Graywolf.Username = strings.TrimSpace(c.Graywolf.Username)
	c.Graywolf.SendPath = strings.ToLower(strings.TrimSpace(c.Graywolf.SendPath))
	if c.Graywolf.SendPath == "" {
		c.Graywolf.SendPath = "rf"
	}
	if c.Station.Tocall == "" {
		c.Station.Tocall = "APZEMC"
	}
	c.KISS.Address = strings.TrimSpace(c.KISS.Address)
	if c.KISS.Address == "" {
		c.KISS.Address = "127.0.0.1:6700"
	}
	c.Storage.ObjectsFile = strings.TrimSpace(c.Storage.ObjectsFile)
	if c.Storage.ObjectsFile == "" {
		c.Storage.ObjectsFile = "objects.json"
	}
	c.Web.Listen = strings.TrimSpace(c.Web.Listen)
	if c.Web.Listen == "" {
		c.Web.Listen = "127.0.0.1:8765"
	}
	if c.Updates.IntervalHours <= 0 {
		c.Updates.IntervalHours = 12
	}
}

// SetupNeeded reports whether the operator still has to enter the one
// thing we cannot default: the station callsign.
func (c Config) SetupNeeded() bool { return strings.TrimSpace(c.Station.Callsign) == "" }

// Validate checks required fields and basic constraints. A config that
// passes Validate can transmit.
func (c Config) Validate() error {
	if c.SetupNeeded() {
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
	switch c.Transport {
	case TransportGraywolf:
		u, err := url.Parse(c.Graywolf.URL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("graywolf.url %q must be an http(s) URL like http://127.0.0.1:8080", c.Graywolf.URL)
		}
		switch c.Graywolf.SendPath {
		case "rf", "both", "is_only":
		default:
			return fmt.Errorf("graywolf.send_path %q must be rf, both or is_only", c.Graywolf.SendPath)
		}
		if c.Graywolf.Channel < 0 {
			return fmt.Errorf("graywolf.channel must be >= 0")
		}
	case TransportKISS:
		if err := validateHostPort(c.KISS.Address); err != nil {
			return fmt.Errorf("kiss.address: %w", err)
		}
		if c.KISS.Port < 0 || c.KISS.Port > 15 {
			return fmt.Errorf("kiss.port %d outside 0-15", c.KISS.Port)
		}
	default:
		return fmt.Errorf("transport %q must be %q or %q", c.Transport, TransportGraywolf, TransportKISS)
	}
	if c.Storage.ObjectsFile == "" {
		return fmt.Errorf("storage.objects_file is required")
	}
	if c.Web.Enabled {
		if err := validateHostPort(c.Web.Listen); err != nil {
			return fmt.Errorf("web.listen: %w", err)
		}
	}
	return nil
}

func validateHostPort(hp string) error {
	host, port, err := net.SplitHostPort(hp)
	if err != nil {
		return fmt.Errorf("%q: %w", hp, err)
	}
	if host == "" {
		return fmt.Errorf("%q has an empty host", hp)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fmt.Errorf("%q has an invalid port", hp)
	}
	return nil
}

// Source returns the parsed source address (zero value if invalid).
func (c Config) Source() ax25.Address { a, _ := ax25.ParseAddress(c.Station.Callsign); return a }

// Dest returns the parsed destination address (zero value if invalid).
func (c Config) Dest() ax25.Address { a, _ := ax25.ParseAddress(c.Station.Tocall); return a }

// Path returns the parsed default digipeater path (nil when direct).
func (c Config) Path() []ax25.Address { p, _ := ax25.ParsePath(c.Station.Path); return p }

// Save writes the configuration atomically (temp file + rename).
func Save(path string, c Config) error {
	c.Normalize()
	data, err := yaml.Marshal(&c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	header := "# emcomm-objects configuration. Edit here or in the app's Settings.\n# Field reference: config.example.yaml / README.md\n"
	return atomicWrite(path, []byte(header+string(data)))
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ExampleYAML is the annotated template written by --init-config and
// shipped as config.example.yaml.
const ExampleYAML = `# emcomm-objects configuration. Copy to config.yaml and edit, or just start
# the app and fill in Settings in the browser.

station:
  # Source callsign with SSID for every object beacon. Use an SSID that is
  # NOT Graywolf's own station SSID (e.g. Graywolf is N0CALL-1, use N0CALL-12).
  callsign: "N0CALL-12"

  # AX.25 destination ("tocall"). APZEMC is an experimental prefix that
  # identifies this app. Change only if you have a registered tocall.
  tocall: "APZEMC"

  # Default digipeater path. Per-object overrides are set in the UI.
  #   ""                 direct (no digipeater)
  #   "WIDE1-1"          1 hop, recommended for fixed objects
  #   "WIDE1-1,WIDE2-1"  2 hops
  path: "WIDE1-1"

# How packets reach the radio:
#   graywolf  Graywolf's REST API (default). Works with a stock Graywolf
#             install: emcomm-objects logs in like the web UI does and asks
#             Graywolf to send object beacons. Graywolf handles AX.25, the
#             channel, digipeating and iGating.
#   kiss      KISS over TCP. Needs a KISS Interface in Graywolf (type TCP,
#             mode "modem", listen 127.0.0.1:6700) or any other KISS TNC.
transport: "graywolf"

graywolf:
  url: "http://127.0.0.1:8080"
  username: ""
  password: ""
  # Graywolf radio channel id to transmit on. 0 = Graywolf's default.
  channel: 0
  # Where Graywolf sends each beacon: "rf", "both" (RF + APRS-IS) or "is_only".
  send_path: "rf"

kiss:
  address: "127.0.0.1:6700"
  # KISS port byte (0-15). Usually 0.
  port: 0

storage:
  # Objects JSON file. Compatible with Pinpoint APRS's pinpointAprsObjects.json.
  # A relative path is inside the data directory.
  objects_file: "objects.json"

web:
  enabled: true
  listen: "127.0.0.1:8765"
  open_browser: true

updates:
  # Check GitHub for a newer release at start and every interval_hours.
  # Only the version number is fetched; nothing about you is sent.
  check: true
  interval_hours: 12
  skip_version: ""
`

// WriteExample writes the annotated template to path.
func WriteExample(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(ExampleYAML), 0o644)
}
