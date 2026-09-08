// Package graywolf talks to Graywolf's REST API (https://github.com/chrissnell/graywolf)
// and implements the "graywolf" transport: one Graywolf beacon per emcomm
// object, fired with POST /api/beacons/{id}/send.
//
// Verified against Graywolf 0.14 (docs/handbook/openapi.json):
//   - POST /api/auth/login {username,password} sets a session cookie.
//   - GET  /api/version is unauthenticated; /api/health is not.
//   - Beacons: GET/POST /api/beacons, GET/PUT/DELETE /api/beacons/{id},
//     POST /api/beacons/{id}/send (send-now ignores `enabled`).
//   - A beacon of type "object" is built by Graywolf from object_name,
//     lat/lon, symbol and comment; type "custom" sends custom_info verbatim
//     (with `comment` appended, so we keep it empty).
//   - slot_seconds must be -1 for "no fixed slot" (0 means "on the hour").
package graywolf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a small cookie-session REST client. Safe for concurrent use.
type Client struct {
	mu       sync.Mutex
	base     string
	user     string
	pass     string
	http     *http.Client
	loggedIn bool
}

// NewClient builds a client for base (e.g. "http://127.0.0.1:8080").
func NewClient(base, user, pass string) *Client {
	jar, _ := cookiejar.New(nil)
	return &Client{
		base: strings.TrimRight(base, "/"),
		user: user,
		pass: pass,
		http: &http.Client{Timeout: 15 * time.Second, Jar: jar},
	}
}

// Reconfigure swaps the endpoint/credentials (Settings changed).
func (c *Client) Reconfigure(base, user, pass string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	base = strings.TrimRight(base, "/")
	if base != c.base || user != c.user || pass != c.pass {
		jar, _ := cookiejar.New(nil)
		c.http.Jar = jar
		c.loggedIn = false
	}
	c.base, c.user, c.pass = base, user, pass
}

// Base returns the configured base URL.
func (c *Client) Base() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("graywolf: HTTP %d: %s", e.Status, e.Message) }

// IsNotFound reports whether err is a 404.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// ErrNoCredentials is returned when a login is needed but none configured.
var ErrNoCredentials = errors.New("graywolf: username/password not configured")

func (c *Client) raw(ctx context.Context, method, path string, body any, out any) (int, error) {
	c.mu.Lock()
	base := c.base
	c.mu.Unlock()
	var rdr io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rdr = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api"+path, rdr)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("graywolf: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		msg := e.Error
		if msg == "" {
			msg = strings.TrimSpace(string(data))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		return resp.StatusCode, &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("graywolf: decode %s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// Login authenticates and caches the session cookie.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	user, pass := c.user, c.pass
	c.mu.Unlock()
	if user == "" {
		return ErrNoCredentials
	}
	_, err := c.raw(ctx, http.MethodPost, "/auth/login", map[string]string{"username": user, "password": pass}, nil)
	c.mu.Lock()
	c.loggedIn = err == nil
	c.mu.Unlock()
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
			return fmt.Errorf("graywolf: login rejected for user %q", user)
		}
		return err
	}
	return nil
}

// do performs an authenticated request, logging in on demand and retrying
// once after a 401 (session expired).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	c.mu.Lock()
	logged := c.loggedIn
	c.mu.Unlock()
	if !logged {
		if err := c.Login(ctx); err != nil {
			return err
		}
	}
	status, err := c.raw(ctx, method, path, body, out)
	if status == http.StatusUnauthorized {
		if err := c.Login(ctx); err != nil {
			return err
		}
		_, err = c.raw(ctx, method, path, body, out)
	}
	return err
}

// Version is GET /api/version (no login needed).
type Version struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Platform string `json:"platform"`
}

// GetVersion asks the server for its version; a failure means Graywolf is
// not reachable at all.
func (c *Client) GetVersion(ctx context.Context) (Version, error) {
	var v Version
	_, err := c.raw(ctx, http.MethodGet, "/version", nil, &v)
	return v, err
}

// Beacon mirrors the subset of dto.BeaconRequest / dto.BeaconResponse we use.
// Fields without omitempty are always sent because their zero value carries
// meaning (enabled=false, channel=0 = default).
type Beacon struct {
	ID          int     `json:"id,omitempty"`
	Type        string  `json:"type"`
	Callsign    string  `json:"callsign"`
	Destination string  `json:"destination,omitempty"`
	Path        string  `json:"path"`
	Channel     int     `json:"channel,omitempty"`
	SendPath    string  `json:"send_path"`
	Enabled     bool    `json:"enabled"`
	Interval    int     `json:"interval"`
	SlotSeconds int     `json:"slot_seconds"`
	ObjectName  string  `json:"object_name,omitempty"`
	UseGPS      bool    `json:"use_gps"`
	Latitude    float64 `json:"latitude"`
	Longitude   float64 `json:"longitude"`
	AltFt       float64 `json:"alt_ft,omitempty"`
	SymbolTable string  `json:"symbol_table,omitempty"`
	Symbol      string  `json:"symbol,omitempty"`
	Overlay     string  `json:"overlay,omitempty"`
	Comment     string  `json:"comment"`
	CustomInfo  string  `json:"custom_info,omitempty"`
	Messaging   bool    `json:"messaging"`
	SmartBeacon bool    `json:"smart_beacon"`
}

// ListBeacons is GET /api/beacons.
func (c *Client) ListBeacons(ctx context.Context) ([]Beacon, error) {
	var out []Beacon
	if err := c.do(ctx, http.MethodGet, "/beacons", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateBeacon is POST /api/beacons.
func (c *Client) CreateBeacon(ctx context.Context, b Beacon) (Beacon, error) {
	var out Beacon
	b.ID = 0
	err := c.do(ctx, http.MethodPost, "/beacons", b, &out)
	return out, err
}

// UpdateBeacon is PUT /api/beacons/{id}.
func (c *Client) UpdateBeacon(ctx context.Context, id int, b Beacon) (Beacon, error) {
	var out Beacon
	b.ID = 0
	err := c.do(ctx, http.MethodPut, fmt.Sprintf("/beacons/%d", id), b, &out)
	return out, err
}

// DeleteBeacon is DELETE /api/beacons/{id}. A 404 is not an error.
func (c *Client) DeleteBeacon(ctx context.Context, id int) error {
	err := c.do(ctx, http.MethodDelete, fmt.Sprintf("/beacons/%d", id), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

// SendBeacon is POST /api/beacons/{id}/send.
func (c *Client) SendBeacon(ctx context.Context, id int) error {
	return c.do(ctx, http.MethodPost, fmt.Sprintf("/beacons/%d/send", id), nil, nil)
}

// Channel is the subset of dto.ChannelResponse the Settings UI shows.
type Channel struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	Mode      string `json:"mode"`
	ModemType string `json:"modem_type"`
	Backing   *struct {
		Summary string `json:"summary"`
		Health  string `json:"health"`
	} `json:"backing,omitempty"`
}

// ListChannels is GET /api/channels.
func (c *Client) ListChannels(ctx context.Context) ([]Channel, error) {
	var out []Channel
	if err := c.do(ctx, http.MethodGet, "/channels", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// TestResult is what the Settings "Test connection" button shows.
type TestResult struct {
	OK       bool      `json:"ok"`
	URL      string    `json:"url"`
	Version  string    `json:"version,omitempty"`
	Platform string    `json:"platform,omitempty"`
	LoggedIn bool      `json:"logged_in"`
	Channels []Channel `json:"channels,omitempty"`
	Beacons  int       `json:"beacons"`
	Error    string    `json:"error,omitempty"`
}

// Test reaches Graywolf, logs in and lists channels. Never returns an error;
// problems are reported in the result.
func (c *Client) Test(ctx context.Context) TestResult {
	r := TestResult{URL: c.Base()}
	v, err := c.GetVersion(ctx)
	if err != nil {
		r.Error = "Graywolf not reachable: " + err.Error()
		return r
	}
	r.Version, r.Platform = v.Version, v.Platform
	if err := c.Login(ctx); err != nil {
		r.Error = err.Error()
		return r
	}
	r.LoggedIn = true
	if chs, err := c.ListChannels(ctx); err == nil {
		r.Channels = chs
	} else {
		r.Error = "channels: " + err.Error()
		return r
	}
	if bs, err := c.ListBeacons(ctx); err == nil {
		r.Beacons = len(bs)
	}
	r.OK = true
	return r
}

// ListStations is GET /api/stations: stations heard in the last timerange
// seconds, optionally only those updated since an RFC3339 timestamp. The
// rows are returned as maps so the bridge forwards Graywolf's DTO verbatim.
func (c *Client) ListStations(ctx context.Context, timerangeSeconds int, since string) ([]map[string]any, error) {
	if timerangeSeconds <= 0 {
		timerangeSeconds = 3600
	}
	path := fmt.Sprintf("/stations?bbox=-90,-180,90,180&timerange=%d", timerangeSeconds)
	if since != "" {
		path += "&since=" + url.QueryEscape(since)
	}
	var out []map[string]any
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SendMessage is POST /api/messages: a direct APRS message to one station.
// Graywolf handles retries and acks; RF first with APRS-IS fallback per its
// own preferences.
func (c *Client) SendMessage(ctx context.Context, to, text string) error {
	to = strings.ToUpper(strings.TrimSpace(to))
	if to == "" || strings.TrimSpace(text) == "" {
		return errors.New("graywolf: message needs a recipient and text")
	}
	return c.do(ctx, http.MethodPost, "/messages", map[string]any{"to": to, "text": text}, nil)
}

// StationCallsign is GET /api/station/config: Graywolf's own callsign-SSID.
func (c *Client) StationCallsign(ctx context.Context) (string, error) {
	var out struct {
		Callsign string `json:"callsign"`
	}
	if err := c.do(ctx, http.MethodGet, "/station/config", nil, &out); err != nil {
		return "", err
	}
	return out.Callsign, nil
}
