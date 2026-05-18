package kiss

import (
	"bytes"
	"io"
	"testing"
)

func TestEncodeDataFrame_Simple(t *testing.T) {
	got, err := EncodeDataFrame(0, []byte{0x01, 0x02, 0x03})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{FEND, 0x00, 0x01, 0x02, 0x03, FEND}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestEncodeDataFrame_Port(t *testing.T) {
	got, err := EncodeDataFrame(3, []byte{0xAA})
	if err != nil {
		t.Fatal(err)
	}
	// Type byte: port=3, cmd=0 → 0x30
	want := []byte{FEND, 0x30, 0xAA, FEND}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestEncodeDataFrame_Stuffing(t *testing.T) {
	// Payload contains both FEND and FESC; both must be escaped.
	got, err := EncodeDataFrame(0, []byte{FEND, 0x42, FESC, 0x43})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{
		FEND, 0x00,
		FESC, TFEND, // escaped FEND
		0x42,
		FESC, TFESC, // escaped FESC
		0x43,
		FEND,
	}
	if !bytes.Equal(got, want) {
		t.Errorf("got %x, want %x", got, want)
	}
}

func TestEncodeDataFrame_BadPort(t *testing.T) {
	if _, err := EncodeDataFrame(16, []byte{0x01}); err == nil {
		t.Error("expected error for port 16")
	}
}

func TestEncodeDataFrame_EmptyPayload(t *testing.T) {
	if _, err := EncodeDataFrame(0, nil); err == nil {
		t.Error("expected error for empty payload")
	}
}

func TestReader_RoundTrip(t *testing.T) {
	originals := [][]byte{
		{0x01, 0x02, 0x03},
		{FEND, FESC, 0x00, 0xFF},
		bytes.Repeat([]byte{FEND}, 10),
	}
	var stream bytes.Buffer
	for _, p := range originals {
		enc, err := EncodeDataFrame(0, p)
		if err != nil {
			t.Fatal(err)
		}
		stream.Write(enc)
	}
	r := NewReader(&stream)
	for i, want := range originals {
		f, err := r.ReadFrame()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if f.Port != 0 || f.Command != CmdData {
			t.Errorf("frame %d: port=%d cmd=%d", i, f.Port, f.Command)
		}
		if !bytes.Equal(f.Payload, want) {
			t.Errorf("frame %d: got %x, want %x", i, f.Payload, want)
		}
	}
	if _, err := r.ReadFrame(); err != io.EOF {
		t.Errorf("expected EOF after stream end, got %v", err)
	}
}

func TestReader_SkipsLeadingFENDs(t *testing.T) {
	// Some senders prefix frames with multiple FENDs as a sync mechanism.
	stream := []byte{FEND, FEND, FEND, 0x00, 0xAA, FEND}
	r := NewReader(bytes.NewReader(stream))
	f, err := r.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Payload) != 1 || f.Payload[0] != 0xAA {
		t.Errorf("payload: %x", f.Payload)
	}
}

func TestReader_BadEscape(t *testing.T) {
	stream := []byte{FEND, 0x00, FESC, 0x99, FEND}
	r := NewReader(bytes.NewReader(stream))
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected error for invalid escape")
	}
}

func TestReader_UnterminatedFrame(t *testing.T) {
	stream := []byte{FEND, 0x00, 0xAA, 0xBB} // no closing FEND
	r := NewReader(bytes.NewReader(stream))
	if _, err := r.ReadFrame(); err == nil {
		t.Error("expected error for unterminated frame")
	}
}
