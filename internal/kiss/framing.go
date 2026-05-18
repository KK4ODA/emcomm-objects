// Package kiss implements KISS framing and a TCP client for talking to a
// KISS-over-TCP TNC (or, in our case, Graywolf's KISS interface).
//
// See "The KISS TNC" by Mike Chepponis & Phil Karn (1987) for the framing
// spec — byte-stuffed frames delimited by FEND, with the first byte of each
// frame indicating port and command.
package kiss

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

const (
	FEND  byte = 0xC0
	FESC  byte = 0xDB
	TFEND byte = 0xDC
	TFESC byte = 0xDD
)

// CmdData is the KISS "data frame" command (low nibble of the type byte).
const CmdData byte = 0x00

// MaxFrameSize caps decoded frame payload to avoid runaway buffers from a
// hostile or buggy peer. AX.25 frames are well under 1 KB in practice.
const MaxFrameSize = 1 << 14 // 16 KiB

// EncodeDataFrame wraps an AX.25 frame in a KISS data frame for the given
// port (0-15), with byte stuffing and FEND delimiters.
func EncodeDataFrame(port byte, payload []byte) ([]byte, error) {
	if port > 15 {
		return nil, fmt.Errorf("kiss port %d > 15", port)
	}
	if len(payload) == 0 {
		return nil, errors.New("kiss: empty payload")
	}
	// Worst-case size: every byte stuffed (rare).
	buf := make([]byte, 0, len(payload)*2+3)
	buf = append(buf, FEND)
	buf = append(buf, (port<<4)|CmdData)
	for _, b := range payload {
		switch b {
		case FEND:
			buf = append(buf, FESC, TFEND)
		case FESC:
			buf = append(buf, FESC, TFESC)
		default:
			buf = append(buf, b)
		}
	}
	buf = append(buf, FEND)
	return buf, nil
}

// Frame is a decoded KISS frame.
type Frame struct {
	Port    byte
	Command byte
	Payload []byte
}

// Reader decodes KISS frames from an underlying stream.
type Reader struct {
	br *bufio.Reader
}

// NewReader wraps r with KISS frame decoding.
func NewReader(r io.Reader) *Reader {
	return &Reader{br: bufio.NewReader(r)}
}

// ReadFrame returns the next KISS frame from the stream. Leading FENDs are
// skipped (empty inter-frame delimiters are legal). Returns io.EOF at clean
// stream end.
func (r *Reader) ReadFrame() (*Frame, error) {
	// Skip FENDs until we find a real byte.
	var first byte
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			return nil, err
		}
		if b != FEND {
			first = b
			break
		}
	}

	port := first >> 4
	cmd := first & 0x0F

	payload := make([]byte, 0, 256)
	for {
		b, err := r.br.ReadByte()
		if err != nil {
			if err == io.EOF {
				return nil, io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b == FEND {
			break
		}
		if b == FESC {
			n, err := r.br.ReadByte()
			if err != nil {
				return nil, fmt.Errorf("kiss: read after FESC: %w", err)
			}
			switch n {
			case TFEND:
				payload = append(payload, FEND)
			case TFESC:
				payload = append(payload, FESC)
			default:
				return nil, fmt.Errorf("kiss: invalid escape sequence FESC 0x%02X", n)
			}
		} else {
			payload = append(payload, b)
		}
		if len(payload) > MaxFrameSize {
			return nil, fmt.Errorf("kiss: frame exceeds %d bytes", MaxFrameSize)
		}
	}

	return &Frame{Port: port, Command: cmd, Payload: payload}, nil
}
