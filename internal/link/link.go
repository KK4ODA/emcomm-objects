// Package link defines the contract between the packet builder (transmit)
// and the thing that actually puts a frame on the air: a KISS TNC or the
// Graywolf REST API. Keeping it as a tiny interface lets the two transports
// coexist in one process and be switched from Settings without a restart.
package link

import (
	"context"
	"errors"

	"github.com/kk4oda/emcomm-objects/internal/ax25"
)

// Status is the coarse connection state shown in the UI.
type Status int

const (
	Disconnected Status = iota
	Connecting
	Connected
)

func (s Status) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Connected:
		return "connected"
	}
	return "disconnected"
}

// State is what the UI displays for the active transport.
type State struct {
	Transport string `json:"transport"` // "graywolf" | "kiss"
	Status    Status `json:"-"`
	StatusStr string `json:"status"` // Status.String(), for JSON
	Detail    string `json:"detail"` // e.g. "Graywolf 0.14.13 at 127.0.0.1:8080" or last error
}

// WithStatus fills the JSON-friendly string field.
func (s State) WithStatus(st Status) State {
	s.Status = st
	s.StatusStr = st.String()
	return s
}

// Packet is one APRS object report ready for transmission. Info is the
// exact APRS info field; the position/symbol fields are duplicated so a
// transport that prefers structured input (Graywolf's object beacons) can
// use them.
type Packet struct {
	Object     string
	Killed     bool
	Source     ax25.Address
	Dest       ax25.Address
	Path       []ax25.Address
	Info       string
	Latitude   float64
	Longitude  float64
	SymbolTab  byte
	SymbolCode byte
	Comment    string
	AltitudeFt float64
	// IntervalSeconds is informational (Graywolf shows it on its beacon card).
	IntervalSeconds int
}

// PathString renders the digipeater path as "WIDE1-1,WIDE2-1".
func (p Packet) PathString() string {
	out := ""
	for i, a := range p.Path {
		if i > 0 {
			out += ","
		}
		out += a.String()
	}
	return out
}

// Link is a transport that can send object packets.
type Link interface {
	// Name is the transport id ("graywolf" or "kiss").
	Name() string
	// Send transmits one packet. Returns ErrNotConnected when the link is
	// down so the scheduler can retry quietly on the next tick.
	Send(ctx context.Context, p Packet) error
	// Retire tells the transport an object is gone for good (deleted, or its
	// kill sequence finished) so it can drop any per-object state it keeps
	// (Graywolf beacons). Unknown names are not an error.
	Retire(ctx context.Context, object string) error
	// State reports the current connection state.
	State() State
}

// ErrNotConnected is returned by Send when the transport is down.
var ErrNotConnected = errors.New("transport not connected")
