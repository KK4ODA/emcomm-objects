package ax25

import (
	"bytes"
	"testing"
)

func mustAddr(t *testing.T, s string) Address {
	t.Helper()
	a, err := ParseAddress(s)
	if err != nil {
		t.Fatalf("ParseAddress(%q): %v", s, err)
	}
	return a
}

func TestParseAddress(t *testing.T) {
	cases := []struct {
		in       string
		wantCall string
		wantSSID byte
		wantErr  bool
	}{
		{"KK4ODA", "KK4ODA", 0, false},
		{"KK4ODA-12", "KK4ODA", 12, false},
		{"kk4oda-1", "KK4ODA", 1, false},
		{" W1ABC-9 ", "W1ABC", 9, false},
		{"", "", 0, true},
		{"TOOLONG7", "", 0, true},
		{"KK4ODA-16", "", 0, true},
		{"KK4ODA-", "", 0, true},
		{"KK4ODA-x", "", 0, true},
		{"BAD!", "", 0, true},
	}
	for _, c := range cases {
		got, err := ParseAddress(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseAddress(%q): expected error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseAddress(%q): unexpected error %v", c.in, err)
			continue
		}
		if got.Call != c.wantCall || got.SSID != c.wantSSID {
			t.Errorf("ParseAddress(%q) = %+v, want call=%q ssid=%d", c.in, got, c.wantCall, c.wantSSID)
		}
	}
}

func TestParsePath(t *testing.T) {
	got, err := ParsePath("WIDE1-1,WIDE2-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Call != "WIDE1" || got[0].SSID != 1 || got[1].Call != "WIDE2" || got[1].SSID != 1 {
		t.Errorf("path: %+v", got)
	}

	if got, err := ParsePath(""); err != nil || got != nil {
		t.Errorf("empty path: got %v err %v", got, err)
	}
}

// TestEncodeUI_NoPath verifies byte-exact output for a minimal frame.
// Reference computed by hand from APRS spec ch. 3 + AX.25 v2.2 §3.12.
//
// Source: KK4ODA-12, Dest: APZEMC, no path, info: ">test"
//
// Address bytes are ASCII shifted left 1, space-padded to 6.
// SSID byte for src/dest in APRS v1: bit7=1 (C), bits6-5=11, bits4-1=SSID, bit0=E (1 if last).
func TestEncodeUI_NoPath(t *testing.T) {
	dest := mustAddr(t, "APZEMC")
	src := mustAddr(t, "KK4ODA-12")
	info := []byte(">test")

	got, err := EncodeUI(dest, src, nil, info)
	if err != nil {
		t.Fatal(err)
	}

	// Dest address: "APZEMC" shifted left 1, SSID byte = 0b11100000 = 0xE0 (C=1, RR=11, SSID=0, E=0)
	// 'A' = 0x41 << 1 = 0x82; 'P' = 0x50 << 1 = 0xA0; 'Z' = 0x5A << 1 = 0xB4;
	// 'E' = 0x45 << 1 = 0x8A; 'M' = 0x4D << 1 = 0x9A; 'C' = 0x43 << 1 = 0x86.
	// SSID octet: 0xE0
	//
	// Source address: "KK4ODA" shifted, SSID byte = 0b11111001 = 0xF9 (C=1, RR=11, SSID=12 → bits 4-1=1100=0x18, plus E=1)
	// 'K' = 0x4B << 1 = 0x96; 'K' = 0x96; '4' = 0x34 << 1 = 0x68;
	// 'O' = 0x4F << 1 = 0x9E; 'D' = 0x44 << 1 = 0x88; 'A' = 0x41 << 1 = 0x82.
	// SSID octet: 0b11100000 | (12<<1) | 1 = 0xE0 | 0x18 | 0x01 = 0xF9.
	//
	// Then control 0x03, PID 0xF0, info ">test".
	want := []byte{
		// dest "APZEMC"
		0x82, 0xA0, 0xB4, 0x8A, 0x9A, 0x86, 0xE0,
		// src "KK4ODA-12" (last)
		0x96, 0x96, 0x68, 0x9E, 0x88, 0x82, 0xF9,
		// control, PID
		0x03, 0xF0,
		// info ">test"
		'>', 't', 'e', 's', 't',
	}
	if !bytes.Equal(got, want) {
		t.Errorf("encoded frame mismatch\n got  %x\n want %x", got, want)
	}
}

// TestEncodeUI_WithPath verifies the E bit moves to the last digipeater and
// path addresses use the H-bit-clear form (bit 7 = 0).
func TestEncodeUI_WithPath(t *testing.T) {
	dest := mustAddr(t, "APZEMC")
	src := mustAddr(t, "KK4ODA-12")
	path, err := ParsePath("WIDE1-1,WIDE2-1")
	if err != nil {
		t.Fatal(err)
	}
	got, err := EncodeUI(dest, src, path, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}

	// Expect 4 addresses (dest, src, 2 path) * 7 bytes + control + PID + 1 info = 31
	if len(got) != 7*4+2+1 {
		t.Fatalf("length: got %d, want %d", len(got), 7*4+3)
	}
	// Source SSID byte should NOT have E bit (path follows): 0xE0|0x18 = 0xF8.
	if got[13] != 0xF8 {
		t.Errorf("src SSID byte: got 0x%02X, want 0xF8 (no E bit when path present)", got[13])
	}
	// First digipeater SSID byte: H=0, RR=11, SSID=1, E=0 → 0x60 | 0x02 = 0x62
	if got[20] != 0x62 {
		t.Errorf("WIDE1-1 SSID byte: got 0x%02X, want 0x62", got[20])
	}
	// Second digipeater SSID byte: H=0, RR=11, SSID=1, E=1 → 0x60 | 0x02 | 0x01 = 0x63
	if got[27] != 0x63 {
		t.Errorf("WIDE2-1 SSID byte: got 0x%02X, want 0x63", got[27])
	}
	// Control and PID at offset 28, 29
	if got[28] != UIControl || got[29] != NoLayer3 {
		t.Errorf("control/PID: got %02X %02X, want %02X %02X", got[28], got[29], UIControl, NoLayer3)
	}
}

func TestEncodeUI_TooLongPath(t *testing.T) {
	dest := mustAddr(t, "APZEMC")
	src := mustAddr(t, "KK4ODA")
	var path []Address
	for i := 0; i < 9; i++ {
		path = append(path, Address{Call: "WIDE1", SSID: 1})
	}
	if _, err := EncodeUI(dest, src, path, []byte("x")); err == nil {
		t.Error("expected error for 9-address path")
	}
}

func TestEncodeUI_EmptyInfo(t *testing.T) {
	dest := mustAddr(t, "APZEMC")
	src := mustAddr(t, "KK4ODA")
	if _, err := EncodeUI(dest, src, nil, nil); err == nil {
		t.Error("expected error for empty info")
	}
}

func TestAddressString(t *testing.T) {
	if got := (Address{Call: "KK4ODA"}).String(); got != "KK4ODA" {
		t.Errorf("got %q", got)
	}
	if got := (Address{Call: "KK4ODA", SSID: 12}).String(); got != "KK4ODA-12" {
		t.Errorf("got %q", got)
	}
}
