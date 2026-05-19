// Package aprs constructs APRS object packets per the APRS Protocol
// Reference v1.0.1, chapter 11 (Object Reports).
//
// Format (uncompressed, with DDHHMMz timestamp):
//
//	;NAMEnnnn *DDHHMMzLLLL.LLN/LLLLL.LLW{symbol}comment
//	│└──9─────┘└──7─────┘└──8──┘│└──9────┘│└── free-form
//	│ name pad │ timestamp │ lat │ table  │ symbol code
//	│
//	└ object indicator (';')
//
// We emit live objects ('*'); killed objects ('_') aren't supported in v1.
package aprs

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// MaxObjectNameLen is the fixed object name field width in an APRS object
// report (spec ch.11).
const MaxObjectNameLen = 9

// Object describes everything needed to construct an APRS object packet.
type Object struct {
	Name        string  // 1..9 chars, will be space-padded to 9
	Latitude    float64 // decimal degrees, -90..90
	Longitude   float64 // decimal degrees, -180..180
	SymbolTable byte    // '/' (primary), '\\' (alternate), or a letter (overlay)
	SymbolCode  byte    // single ASCII char
	Comment     string  // free-form, appended after symbol code
	AltitudeFt  float64 // 0 = omit; >0 inserts "/A=DDDDDD" at start of comment
}

// Validate checks field ranges and character constraints.
func (o Object) Validate() error {
	name := strings.TrimSpace(o.Name)
	if name == "" {
		return errors.New("object name is empty")
	}
	if len(name) > MaxObjectNameLen {
		return fmt.Errorf("object name %q exceeds %d chars", name, MaxObjectNameLen)
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c < 0x20 || c > 0x7E {
			return fmt.Errorf("object name %q has non-printable char 0x%02X", name, c)
		}
	}
	if o.Latitude < -90 || o.Latitude > 90 {
		return fmt.Errorf("latitude %v outside [-90, 90]", o.Latitude)
	}
	if o.Longitude < -180 || o.Longitude > 180 {
		return fmt.Errorf("longitude %v outside [-180, 180]", o.Longitude)
	}
	if o.SymbolTable == 0 {
		return errors.New("symbol table is empty")
	}
	if o.SymbolCode == 0 {
		return errors.New("symbol code is empty")
	}
	if o.AltitudeFt < 0 || o.AltitudeFt > 999999 {
		return fmt.Errorf("altitude %v ft outside [0, 999999]", o.AltitudeFt)
	}
	// Comment must be plain ASCII printable (the spec allows more, but for
	// emcomm use we keep it conservative — odd bytes confuse old TNCs).
	for i := 0; i < len(o.Comment); i++ {
		c := o.Comment[i]
		if c < 0x20 || c > 0x7E {
			return fmt.Errorf("comment has non-printable char 0x%02X at index %d", c, i)
		}
	}
	return nil
}

// Live/killed indicator bytes per APRS spec ch.11.
const (
	indicatorLive   byte = '*'
	indicatorKilled byte = '_'
)

// BuildPacket formats an APRS LIVE object packet info field.
// Equivalent to buildObjectPacket(o, now, live=true).
func BuildPacket(o Object, now time.Time) (string, error) {
	return buildObjectPacket(o, now, indicatorLive)
}

// BuildKilledPacket formats an APRS KILLED object packet info field. Same
// structure as a live packet but with '_' in place of '*'. Receivers that
// previously saw the live object will mark it as killed/expired and remove
// it from their map.
//
// Per spec, the killed packet must come from the SAME source callsign that
// originated the object. Sending a kill from a different SSID is a no-op:
// receivers can't tell it's "the same object" and ignore it.
func BuildKilledPacket(o Object, now time.Time) (string, error) {
	return buildObjectPacket(o, now, indicatorKilled)
}

// buildObjectPacket is the shared implementation. `now` is the timestamp
// that goes in the packet (UTC-converted internally).
func buildObjectPacket(o Object, now time.Time, indicator byte) (string, error) {
	if err := o.Validate(); err != nil {
		return "", err
	}
	name := padName(o.Name)
	ts := formatTimestampDHM(now.UTC())
	lat := formatLatitude(o.Latitude)
	lon := formatLongitude(o.Longitude)

	var sb strings.Builder
	sb.Grow(64 + len(o.Comment))
	sb.WriteByte(';')
	sb.WriteString(name)
	sb.WriteByte(indicator)
	sb.WriteString(ts)
	sb.WriteString(lat)
	sb.WriteByte(o.SymbolTable)
	sb.WriteString(lon)
	sb.WriteByte(o.SymbolCode)
	if o.AltitudeFt > 0 {
		fmt.Fprintf(&sb, "/A=%06d", int(math.Round(o.AltitudeFt)))
	}
	sb.WriteString(o.Comment)
	return sb.String(), nil
}

// padName pads/truncates name to exactly 9 chars with spaces on the right.
func padName(name string) string {
	name = strings.TrimSpace(name)
	if len(name) > MaxObjectNameLen {
		name = name[:MaxObjectNameLen]
	}
	if len(name) < MaxObjectNameLen {
		name += strings.Repeat(" ", MaxObjectNameLen-len(name))
	}
	return name
}

// formatTimestampDHM returns the 7-char DDHHMMz timestamp (UTC).
func formatTimestampDHM(t time.Time) string {
	t = t.UTC()
	return fmt.Sprintf("%02d%02d%02dz", t.Day(), t.Hour(), t.Minute())
}

// formatLatitude returns the 8-char latitude in APRS uncompressed format:
// DDMM.mmH where H is N or S. Handles minute rounding overflow (e.g. 59.999
// rounding to 60.00 carries into degrees).
func formatLatitude(lat float64) string {
	hemi := byte('N')
	if lat < 0 {
		hemi = 'S'
		lat = -lat
	}
	deg := int(math.Floor(lat))
	min := (lat - float64(deg)) * 60
	deg, min = carryMinutes(deg, min)
	// %05.2f produces e.g. "48.07" (2.2f doesn't zero-pad the integer part).
	return fmt.Sprintf("%02d%05.2f%c", deg, min, hemi)
}

// formatLongitude returns the 9-char longitude in APRS uncompressed format:
// DDDMM.mmH where H is E or W.
func formatLongitude(lon float64) string {
	hemi := byte('E')
	if lon < 0 {
		hemi = 'W'
		lon = -lon
	}
	deg := int(math.Floor(lon))
	min := (lon - float64(deg)) * 60
	deg, min = carryMinutes(deg, min)
	return fmt.Sprintf("%03d%05.2f%c", deg, min, hemi)
}

// carryMinutes handles the case where rounded minutes hit 60.00, in which
// case we bump the degrees field and zero the minutes. We pre-round to 2dp
// here so the subsequent %.2f format doesn't itself overflow.
func carryMinutes(deg int, min float64) (int, float64) {
	rounded := math.Round(min*100) / 100
	if rounded >= 60.0 {
		deg++
		rounded = 0
	}
	return deg, rounded
}
