// Package transmit glues the APRS object builder, AX.25 encoder, and KISS
// client into a single TransmitFunc compatible with the scheduler.
package transmit

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/aprs"
	"github.com/kk4oda/emcomm-objects/internal/ax25"
	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/kiss"
	"github.com/kk4oda/emcomm-objects/internal/store"
)

// Sender constructs and writes an APRS object beacon over a KISS client.
type Sender struct {
	cfg    config.Config
	client *kiss.Client
	log    *slog.Logger

	// Hook for observers (web UI live log, tests).
	OnPacket func(PacketEvent)
}

// PacketEvent captures one transmitted beacon for the UI/log.
type PacketEvent struct {
	When     time.Time
	Object   string
	Killed   bool // true if this was a kill packet ('_' indicator), not a live beacon
	Source   ax25.Address
	Dest     ax25.Address
	Path     []ax25.Address
	Info     string // APRS info field (the human-readable bit)
	NumBytes int    // total KISS-framed byte count
}

// NewSender wires a Sender from config and a connected KISS client.
func NewSender(cfg config.Config, client *kiss.Client, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}
	return &Sender{cfg: cfg, client: client, log: log}
}

// Transmit builds and sends a single LIVE beacon for the given object.
// Suitable as the TransmitFunc passed to scheduler.New.
func (s *Sender) Transmit(o store.Object, now time.Time) error {
	return s.send(o, now, false)
}

// TransmitKilled builds and sends a single KILLED beacon (APRS '_' indicator)
// for the given object. Same AX.25 wrapping as a live beacon; only the
// indicator byte in the APRS info field differs.
func (s *Sender) TransmitKilled(o store.Object, now time.Time) error {
	return s.send(o, now, true)
}

// send is the shared implementation. killed=true selects '_' indicator.
func (s *Sender) send(o store.Object, now time.Time, killed bool) error {
	src := s.cfg.Source()
	dest := s.cfg.Dest()
	path := s.cfg.Path()
	// Per-object path override. Two sentinels:
	//   ""  → use station default (already loaded above)
	//   "-" → explicit direct override (no digipeater path)
	// Any other value is parsed as a real path.
	if o.Path == "-" {
		path = nil
	} else if o.Path != "" {
		p, err := ax25.ParsePath(o.Path)
		if err != nil {
			return fmt.Errorf("object %q path: %w", o.ObjectName, err)
		}
		path = p
	}

	apr := aprs.Object{
		Name:        o.ObjectName,
		Latitude:    o.Latitude,
		Longitude:   o.Longitude,
		SymbolTable: o.SymbolTableByte(),
		SymbolCode:  o.SymbolCodeByte(),
		Comment:     o.Comment,
		AltitudeFt:  o.Altitude,
	}

	var info string
	var err error
	if killed {
		info, err = aprs.BuildKilledPacket(apr, now)
	} else {
		info, err = aprs.BuildPacket(apr, now)
	}
	if err != nil {
		return fmt.Errorf("object %q: build packet: %w", o.ObjectName, err)
	}

	frame, err := ax25.EncodeUI(dest, src, path, []byte(info))
	if err != nil {
		return fmt.Errorf("object %q: encode AX.25: %w", o.ObjectName, err)
	}
	if err := s.client.SendAX25(frame); err != nil {
		return fmt.Errorf("object %q: send: %w", o.ObjectName, err)
	}
	kind := "live"
	if killed {
		kind = "killed"
	}
	s.log.Info("transmitted",
		"object", o.ObjectName,
		"kind", kind,
		"src", src.String(),
		"dest", dest.String(),
		"path", o.Path,
		"info", info,
	)
	if s.OnPacket != nil {
		s.OnPacket(PacketEvent{
			When:     now,
			Object:   o.ObjectName,
			Killed:   killed,
			Source:   src,
			Dest:     dest,
			Path:     path,
			Info:     info,
			NumBytes: len(frame) + 4, // +KISS overhead estimate (FEND/type/FEND)
		})
	}
	return nil
}
