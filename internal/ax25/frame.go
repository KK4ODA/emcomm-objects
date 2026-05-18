// Package ax25 encodes AX.25 v2 UI frames for APRS.
//
// Only the encoding side is implemented; receiving/decoding lives in Graywolf.
// Frames are returned without an FCS — KISS handles framing and the modem
// computes the FCS on transmit.
package ax25

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// AX.25 control field constants used for APRS.
const (
	UIControl byte = 0x03 // Unnumbered Information frame
	NoLayer3  byte = 0xF0 // Protocol ID: no layer 3
)

// Address is one AX.25 address (callsign + SSID).
type Address struct {
	Call string // 1-6 chars, uppercase ASCII letters/digits
	SSID byte   // 0-15
}

// String formats an address as "CALL" or "CALL-SSID".
func (a Address) String() string {
	if a.SSID == 0 {
		return a.Call
	}
	return fmt.Sprintf("%s-%d", a.Call, a.SSID)
}

// ParseAddress parses "CALLSIGN" or "CALLSIGN-SSID".
func ParseAddress(s string) (Address, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return Address{}, errors.New("empty address")
	}
	var a Address
	if i := strings.IndexByte(s, '-'); i >= 0 {
		a.Call = s[:i]
		ssidStr := s[i+1:]
		if ssidStr == "" {
			return Address{}, errors.New("missing SSID after '-'")
		}
		n, err := strconv.Atoi(ssidStr)
		if err != nil {
			return Address{}, fmt.Errorf("invalid SSID %q: %w", ssidStr, err)
		}
		if n < 0 || n > 15 {
			return Address{}, fmt.Errorf("SSID %d out of range 0-15", n)
		}
		a.SSID = byte(n)
	} else {
		a.Call = s
	}
	if err := validate(a); err != nil {
		return Address{}, err
	}
	return a, nil
}

// ParsePath parses a comma-separated digipeater path like "WIDE1-1,WIDE2-1".
// Returns nil for an empty string.
func ParsePath(s string) ([]Address, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]Address, 0, len(parts))
	for i, p := range parts {
		a, err := ParseAddress(p)
		if err != nil {
			return nil, fmt.Errorf("path[%d]: %w", i, err)
		}
		out = append(out, a)
	}
	if len(out) > 8 {
		return nil, fmt.Errorf("path has %d addresses, AX.25 max is 8", len(out))
	}
	return out, nil
}

// EncodeUI returns the raw AX.25 UI frame bytes (no FCS, no KISS framing)
// for an APRS-style packet:
//
//	src > dest,path... : info
//
// Bit conventions follow APRS-IS / AX.25 v2 practice: the command/response
// bits on both dest and source SSID octets are set (legacy v1 style, accepted
// by every APRS implementation in the wild). Path addresses are emitted with
// the H ("has been repeated") bit cleared.
func EncodeUI(dest, src Address, path []Address, info []byte) ([]byte, error) {
	if err := validate(dest); err != nil {
		return nil, fmt.Errorf("dest: %w", err)
	}
	if err := validate(src); err != nil {
		return nil, fmt.Errorf("src: %w", err)
	}
	if len(path) > 8 {
		return nil, fmt.Errorf("path has %d addresses, AX.25 max is 8", len(path))
	}
	for i, p := range path {
		if err := validate(p); err != nil {
			return nil, fmt.Errorf("path[%d]: %w", i, err)
		}
	}
	if len(info) == 0 {
		return nil, errors.New("info field is empty")
	}

	buf := make([]byte, 0, 7*(2+len(path))+2+len(info))

	// Destination first, then source. APRS v1 sets both C bits high.
	writeAddr(&buf, dest, true, false)
	writeAddr(&buf, src, true, len(path) == 0)

	// Digipeaters. H bit cleared (not yet repeated). E bit on last only.
	for i, p := range path {
		writeAddr(&buf, p, false, i == len(path)-1)
	}

	buf = append(buf, UIControl, NoLayer3)
	buf = append(buf, info...)
	return buf, nil
}

// writeAddr appends the 7-byte address field.
// highBit: bit 7 of the SSID octet (C bit for src/dest, H bit for path).
// last: end-of-address bit (bit 0 of SSID octet).
func writeAddr(buf *[]byte, a Address, highBit, last bool) {
	for i := 0; i < 6; i++ {
		var c byte = ' '
		if i < len(a.Call) {
			c = a.Call[i]
		}
		*buf = append(*buf, c<<1)
	}
	// SSID octet: bit7=C/H, bits6-5=reserved (1,1), bits4-1=SSID, bit0=E
	var ssid byte = 0b01100000 | ((a.SSID & 0x0F) << 1)
	if highBit {
		ssid |= 0x80
	}
	if last {
		ssid |= 0x01
	}
	*buf = append(*buf, ssid)
}

func validate(a Address) error {
	if len(a.Call) == 0 || len(a.Call) > 6 {
		return fmt.Errorf("call %q must be 1-6 chars", a.Call)
	}
	if a.SSID > 15 {
		return fmt.Errorf("SSID %d out of range 0-15", a.SSID)
	}
	for i := 0; i < len(a.Call); i++ {
		c := a.Call[i]
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return fmt.Errorf("call %q has invalid character %q (only A-Z, 0-9)", a.Call, c)
		}
	}
	return nil
}
