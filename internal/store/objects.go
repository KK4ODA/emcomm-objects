// Package store persists the list of APRS objects as a JSON file that is
// read- and write-compatible with Pinpoint APRS's pinpointAprsObjects.json
// format. Additional fields (Enabled, Path) are added; Pinpoint ignores
// unknown fields, so the file remains usable by both tools.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// utf8BOM is what Windows editors (Notepad, PowerShell Out-File -Encoding utf8)
// prepend to "UTF-8" text files. encoding/json doesn't strip it.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// Pinpoint's zero value for time fields.
const PinpointZeroTime = "0001-01-01T00:00:00"

// LooseTime is time.Time with relaxed JSON parsing: it accepts both RFC3339
// (Go's default) and the timezone-less "2006-01-02T15:04:05" format that
// Pinpoint APRS writes. Marshals back in RFC3339 form.
type LooseTime struct{ time.Time }

var looseTimeFormats = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999999",
}

func (t *LooseTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || len(data) < 2 {
		t.Time = time.Time{}
		return nil
	}
	s := strings.Trim(string(data), `"`)
	if s == "" {
		t.Time = time.Time{}
		return nil
	}
	for _, layout := range looseTimeFormats {
		if parsed, err := time.Parse(layout, s); err == nil {
			t.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("LastBeacon: unrecognised time format %q", s)
}

func (t LooseTime) MarshalJSON() ([]byte, error) {
	if t.Time.IsZero() {
		return []byte(`"` + PinpointZeroTime + `"`), nil
	}
	return []byte(`"` + t.Time.UTC().Format(time.RFC3339) + `"`), nil
}

// Object status constants. Stored as a string for forward-compat (Pinpoint
// preserves but ignores; older emcomm-objects versions just see "live").
const (
	StatusLive   = "live"   // normal operation; beacon on interval
	StatusKilled = "killed" // killed; draining KillBeaconsLeft, then silent
)

// Object mirrors Pinpoint's JSON shape and adds emcomm-specific fields.
type Object struct {
	ObjectName      string    `json:"ObjectName"`
	ShowTooltip     bool      `json:"ShowTooltip"`
	SymbolTable     string    `json:"SymbolTable"`     // single char, e.g. "/"
	SymbolID        string    `json:"SymbolID"`        // single char, e.g. "r"
	Comment         string    `json:"Comment"`
	IntervalMinutes int       `json:"IntervalMinutes"` // 0 disables auto-beacon
	Latitude        float64   `json:"Latitude"`
	Longitude       float64   `json:"Longitude"`
	Altitude        float64   `json:"Altitude"`        // feet
	LastBeacon      LooseTime `json:"LastBeacon"`      // zero = never

	// emcomm-objects additions (ignored by Pinpoint):
	Enabled bool   `json:"Enabled"`
	Path    string `json:"Path,omitempty"` // "" = use station default, "-" = direct

	// Lifecycle / kill semantics. All omitempty so files written by older
	// versions of this app, or by Pinpoint, round-trip cleanly.
	ExpiresAt       LooseTime `json:"ExpiresAt,omitempty"`       // zero = never auto-expires
	Status          string    `json:"Status,omitempty"`          // "" treated as "live"
	KillBeaconsLeft int       `json:"KillBeaconsLeft,omitempty"` // count of remaining kill TX
	KilledAt        LooseTime `json:"KilledAt,omitempty"`        // when transition to killed happened
}

// StatusOrDefault returns the object's status with empty treated as "live".
// Use this everywhere instead of comparing o.Status directly to handle
// files written before the Status field existed.
func (o Object) StatusOrDefault() string {
	if o.Status == "" {
		return StatusLive
	}
	return o.Status
}

// IsKilled reports whether the object has been killed (regardless of whether
// its kill-beacon sequence has finished draining).
func (o Object) IsKilled() bool { return o.StatusOrDefault() == StatusKilled }

// NeverBeaconed reports whether this object has never had a live beacon
// transmitted. Used to decide whether sending a kill packet would create
// a ghost on receivers (don't kill what nobody knows about).
func (o Object) NeverBeaconed() bool { return o.LastBeacon.IsZero() }

// Validate checks required fields and basic constraints.
func (o Object) Validate() error {
	if strings.TrimSpace(o.ObjectName) == "" {
		return errors.New("ObjectName is required")
	}
	if len(o.ObjectName) > 9 {
		return fmt.Errorf("ObjectName %q exceeds APRS limit of 9 chars", o.ObjectName)
	}
	if len(o.SymbolTable) != 1 {
		return fmt.Errorf("SymbolTable %q must be exactly 1 character", o.SymbolTable)
	}
	if len(o.SymbolID) != 1 {
		return fmt.Errorf("SymbolID %q must be exactly 1 character", o.SymbolID)
	}
	if o.Latitude < -90 || o.Latitude > 90 {
		return fmt.Errorf("Latitude %v out of range", o.Latitude)
	}
	if o.Longitude < -180 || o.Longitude > 180 {
		return fmt.Errorf("Longitude %v out of range", o.Longitude)
	}
	if o.IntervalMinutes < 0 {
		return fmt.Errorf("IntervalMinutes %d must be >= 0", o.IntervalMinutes)
	}
	if o.Status != "" && o.Status != StatusLive && o.Status != StatusKilled {
		return fmt.Errorf("Status %q must be %q or %q (or empty)", o.Status, StatusLive, StatusKilled)
	}
	if o.KillBeaconsLeft < 0 {
		return fmt.Errorf("KillBeaconsLeft %d must be >= 0", o.KillBeaconsLeft)
	}
	return nil
}

// SymbolTableByte returns the SymbolTable as a single byte.
// Validate must have passed.
func (o Object) SymbolTableByte() byte { return o.SymbolTable[0] }

// SymbolCodeByte returns the SymbolID as a single byte.
// Validate must have passed.
func (o Object) SymbolCodeByte() byte { return o.SymbolID[0] }

// Store is a thread-safe, file-backed list of objects.
type Store struct {
	path string

	mu      sync.RWMutex
	objects []Object
}

// New constructs an empty Store. Use Load to populate from disk.
func New(path string) *Store { return &Store{path: path} }

// Load reads the JSON file. A missing file is treated as an empty list.
// Invalid objects in the file are returned as errors but valid ones are
// loaded; partial load lets the user fix the file without losing state.
func (s *Store) Load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.mu.Lock()
			s.objects = nil
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	if len(data) == 0 {
		s.mu.Lock()
		s.objects = nil
		s.mu.Unlock()
		return nil
	}
	var raw []Object
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields() // safe: we model all Pinpoint fields
	// Re-enable lenient mode on retry — Pinpoint may add fields later.
	if err := dec.Decode(&raw); err != nil {
		// Fall back to lenient decode.
		raw = raw[:0]
		if err2 := json.Unmarshal(data, &raw); err2 != nil {
			return fmt.Errorf("parse %s: %w", s.path, err2)
		}
	}
	if err := dec.Decode(new(any)); err != nil && err != io.EOF {
		// Trailing garbage after JSON array. Tolerate.
	}
	s.mu.Lock()
	s.objects = raw
	s.mu.Unlock()
	return nil
}

// Save writes the current object list to disk atomically (write to temp +
// rename). Pretty-printed for human editing.
func (s *Store) Save() error {
	s.mu.RLock()
	out := make([]Object, len(s.objects))
	copy(out, s.objects)
	s.mu.RUnlock()

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".objects-*.json.tmp")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// List returns a copy of the current object list, sorted by name.
func (s *Store) List() []Object {
	s.mu.RLock()
	out := make([]Object, len(s.objects))
	copy(out, s.objects)
	s.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].ObjectName < out[j].ObjectName })
	return out
}

// Get returns the object with the given name (case-sensitive, as APRS object
// names are). The second return value is false if not found.
func (s *Store) Get(name string) (Object, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, o := range s.objects {
		if o.ObjectName == name {
			return o, true
		}
	}
	return Object{}, false
}

// Upsert adds or replaces an object by name. Validates before persisting.
// Lifecycle fields (LastBeacon, Status, KillBeaconsLeft, KilledAt) are
// preserved from the existing row even if the caller didn't set them —
// these are scheduler-managed and a UI edit form must not be able to
// accidentally resurrect a killed object or wipe its kill progress.
// To explicitly transition to killed, use StartKill. To bring back a
// killed object, use Resurrect (when it exists).
func (s *Store) Upsert(o Object) error {
	if err := o.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == o.ObjectName {
			if o.LastBeacon.IsZero() {
				o.LastBeacon = ex.LastBeacon
			}
			// Lifecycle fields are never overwritten by Upsert — only by
			// the dedicated methods below.
			o.Status = ex.Status
			o.KillBeaconsLeft = ex.KillBeaconsLeft
			o.KilledAt = ex.KilledAt
			s.objects[i] = o
			return nil
		}
	}
	s.objects = append(s.objects, o)
	return nil
}

// Delete removes an object by name. Returns false if it didn't exist.
func (s *Store) Delete(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == name {
			s.objects = append(s.objects[:i], s.objects[i+1:]...)
			return true
		}
	}
	return false
}

// MarkBeaconed updates LastBeacon to t for the given object.
// Returns false if the object doesn't exist.
func (s *Store) MarkBeaconed(name string, t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == name {
			s.objects[i].LastBeacon = LooseTime{t}
			return true
		}
	}
	return false
}

// StartKill transitions an object to killed status with the given number
// of kill beacons remaining. Idempotent: if the object is already killed,
// returns ErrAlreadyKilled and does not reset the counter (so an accidental
// second click doesn't re-arm a draining sequence). Returns ErrNotFound if
// the object doesn't exist.
//
// killBeacons may be 0 — useful for the "object auto-expired but was never
// live-beaconed" case, where we want to mark it dead locally but must not
// broadcast a kill (which would create a ghost on receivers).
func (s *Store) StartKill(name string, killBeacons int, now time.Time) error {
	if killBeacons < 0 {
		return fmt.Errorf("StartKill: killBeacons must be >= 0, got %d", killBeacons)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == name {
			if ex.StatusOrDefault() == StatusKilled {
				return ErrAlreadyKilled
			}
			s.objects[i].Status = StatusKilled
			s.objects[i].KillBeaconsLeft = killBeacons
			s.objects[i].KilledAt = LooseTime{now}
			// Reset LastBeacon so the scheduler's "is it time?" check fires
			// the first kill immediately on the next tick.
			s.objects[i].LastBeacon = LooseTime{}
			return nil
		}
	}
	return ErrNotFound
}

// DecrementKillBeacon decrements KillBeaconsLeft by 1 (floor 0) and sets
// LastBeacon to t. Use this after successfully transmitting a kill packet.
// Returns false if the object doesn't exist.
func (s *Store) DecrementKillBeacon(name string, t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == name {
			if s.objects[i].KillBeaconsLeft > 0 {
				s.objects[i].KillBeaconsLeft--
			}
			s.objects[i].LastBeacon = LooseTime{t}
			return true
		}
	}
	return false
}

// Resurrect transitions a killed object back to live, clearing all kill
// state and ExpiresAt (so a now-past expiry doesn't immediately re-kill it),
// and zeroing LastBeacon so the next scheduler tick fires a live beacon
// right away. Returns ErrNotKilled if the object is already live —
// reviving a live object is a no-op error to prevent accidental clicks
// resetting an in-flight live object's LastBeacon.
//
// Operator must re-set ExpiresAt afterward if they want the revived object
// to auto-expire again.
func (s *Store) Resurrect(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, ex := range s.objects {
		if ex.ObjectName == name {
			if !ex.IsKilled() {
				return ErrNotKilled
			}
			s.objects[i].Status = StatusLive
			s.objects[i].KillBeaconsLeft = 0
			s.objects[i].KilledAt = LooseTime{}
			s.objects[i].ExpiresAt = LooseTime{}
			s.objects[i].LastBeacon = LooseTime{} // fire fresh live beacon next tick
			return nil
		}
	}
	return ErrNotFound
}

// Standard errors for kill operations. Callers compare with errors.Is.
var (
	ErrNotFound      = errors.New("object not found")
	ErrAlreadyKilled = errors.New("object already killed")
	ErrNotKilled     = errors.New("object is not killed; nothing to revive")
	ErrNeverBeaconed = errors.New("object was never live-beaconed; refusing to send kill (would create a ghost)")
	ErrNoKillBeacons = errors.New("object has no remaining kill beacons")
)
