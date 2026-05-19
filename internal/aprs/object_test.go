package aprs

import (
	"strings"
	"testing"
	"time"
)

func TestBuildPacket_DCFR1(t *testing.T) {
	// From the Pinpoint sample JSON in the design brief.
	o := Object{
		Name:        "DCFR_1",
		Latitude:    33.8011357,
		Longitude:   -84.3301389,
		SymbolTable: '/',
		SymbolCode:  'r',
		Comment:     "DCFR Station 1",
	}
	now := time.Date(2026, 5, 17, 21, 23, 0, 0, time.UTC)
	got, err := BuildPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}

	// 33.8011357 → 33° + 0.8011357*60 = 48.0681' → 48.07 (rounded)
	// -84.3301389 → 84° W + 0.3301389*60 = 19.8083' → 19.81 (rounded)
	want := ";DCFR_1   *172123z3348.07N/08419.81Wr" + "DCFR Station 1"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}

	// Spot-check field widths.
	if len(got[1:10]) != 9 {
		t.Errorf("object name field width: got %q", got[1:10])
	}
	if got[10] != '*' {
		t.Errorf("expected live object indicator '*', got %q", got[10])
	}
	if got[17] != 'z' {
		t.Errorf("timestamp suffix: got %q", got[17])
	}
}

func TestBuildPacket_DCFR2_SymbolPosition(t *testing.T) {
	o := Object{
		Name:        "DCFR_2",
		Latitude:    33.8604167,
		Longitude:   -84.3350799,
		SymbolTable: '/',
		SymbolCode:  'r',
		Comment:     "DCFR Station 2",
	}
	now := time.Date(2026, 5, 17, 21, 23, 0, 0, time.UTC)
	got, err := BuildPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	// 33.8604167 → 33° + 51.625' → 51.63
	// 84.3350799 → 84° + 20.1048' → 20.10
	want := ";DCFR_2   *172123z3351.63N/08420.10Wr" + "DCFR Station 2"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
}

func TestBuildPacket_NamePadding(t *testing.T) {
	o := validObject()
	o.Name = "X" // 1 char, should pad to 9
	got, err := BuildPacket(o, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// After ';' the next 9 chars are the name field: "X        "
	if got[1:10] != "X        " {
		t.Errorf("name padding: got %q", got[1:10])
	}
}

func TestBuildPacket_NameTooLong(t *testing.T) {
	o := validObject()
	o.Name = "ABCDEFGHIJ" // 10 chars
	if _, err := BuildPacket(o, time.Now()); err == nil {
		t.Error("expected error for 10-char name")
	}
}

func TestBuildPacket_SouthHemisphere(t *testing.T) {
	o := validObject()
	o.Latitude = -33.8011357
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "3348.07S") {
		t.Errorf("expected S hemisphere: %s", got)
	}
}

func TestBuildPacket_EastHemisphere(t *testing.T) {
	o := validObject()
	o.Longitude = 84.3301389
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "08419.81E") {
		t.Errorf("expected E hemisphere: %s", got)
	}
}

func TestBuildPacket_MinuteCarry(t *testing.T) {
	// 33.99999999° lat: minutes = 59.9999994 → rounds to 60.00 → carry to 34°00.00'
	o := validObject()
	o.Latitude = 33.99999999
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "3400.00N") {
		t.Errorf("expected carry to 3400.00N: %s", got)
	}
}

func TestBuildPacket_Altitude(t *testing.T) {
	o := validObject()
	o.AltitudeFt = 1234
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/A=001234") {
		t.Errorf("expected altitude tag: %s", got)
	}
}

func TestBuildPacket_NoAltitudeWhenZero(t *testing.T) {
	o := validObject()
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "/A=") {
		t.Errorf("expected no altitude tag: %s", got)
	}
}

func TestBuildPacket_EmptyComment(t *testing.T) {
	o := validObject()
	o.Comment = ""
	got, err := BuildPacket(o, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	// Should still end cleanly at the symbol code.
	if !strings.HasSuffix(got, "r") {
		t.Errorf("expected to end at symbol: %s", got)
	}
}

func TestBuildPacket_InvalidLatLon(t *testing.T) {
	o := validObject()
	o.Latitude = 91
	if _, err := BuildPacket(o, time.Now()); err == nil {
		t.Error("expected error for lat>90")
	}
	o = validObject()
	o.Longitude = -181
	if _, err := BuildPacket(o, time.Now()); err == nil {
		t.Error("expected error for lon<-180")
	}
}

func TestBuildPacket_InvalidName(t *testing.T) {
	o := validObject()
	o.Name = ""
	if _, err := BuildPacket(o, time.Now()); err == nil {
		t.Error("expected error for empty name")
	}
	o = validObject()
	o.Name = "BAD\x01"
	if _, err := BuildPacket(o, time.Now()); err == nil {
		t.Error("expected error for non-printable char in name")
	}
}

func TestBuildPacket_TimestampFormat(t *testing.T) {
	o := validObject()
	now := time.Date(2026, 5, 7, 3, 5, 9, 0, time.UTC)
	got, err := BuildPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	// Day=07, Hour=03, Minute=05 → "070305z"
	if got[11:18] != "070305z" {
		t.Errorf("timestamp: got %q", got[11:18])
	}
}

func TestBuildPacket_NonUTCTimeConverted(t *testing.T) {
	o := validObject()
	loc, _ := time.LoadLocation("America/New_York")
	// 2026-05-17 17:23:00 EDT == 2026-05-17 21:23:00 UTC
	now := time.Date(2026, 5, 17, 17, 23, 0, 0, loc)
	got, err := BuildPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	if got[11:18] != "172123z" {
		t.Errorf("expected UTC conversion to 172123z, got %q", got[11:18])
	}
}

func TestBuildKilledPacket_Format(t *testing.T) {
	// Killed packet: same shape as live, but with '_' in place of '*'.
	o := validObject()
	now := time.Date(2026, 5, 19, 21, 23, 0, 0, time.UTC)
	live, err := BuildPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	killed, err := BuildKilledPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	// Should differ in exactly one byte: position 10 (after ';' + 9-char name).
	if len(live) != len(killed) {
		t.Fatalf("length mismatch: live=%d killed=%d", len(live), len(killed))
	}
	if live[10] != '*' || killed[10] != '_' {
		t.Errorf("indicator bytes wrong: live[10]=%q killed[10]=%q", live[10], killed[10])
	}
	// All other bytes identical.
	for i := 0; i < len(live); i++ {
		if i == 10 {
			continue
		}
		if live[i] != killed[i] {
			t.Errorf("byte %d differs unexpectedly: live=%q killed=%q", i, live[i], killed[i])
		}
	}
}

func TestBuildKilledPacket_DCFR1(t *testing.T) {
	o := Object{
		Name: "DCFR_1", Latitude: 33.8011357, Longitude: -84.3301389,
		SymbolTable: '/', SymbolCode: 'r', Comment: "DCFR Station 1",
	}
	now := time.Date(2026, 5, 19, 21, 23, 0, 0, time.UTC)
	got, err := BuildKilledPacket(o, now)
	if err != nil {
		t.Fatal(err)
	}
	want := ";DCFR_1   _192123z3348.07N/08419.81WrDCFR Station 1"
	if got != want {
		t.Errorf("\n got  %q\n want %q", got, want)
	}
}

func validObject() Object {
	return Object{
		Name:        "TEST",
		Latitude:    33.8,
		Longitude:   -84.3,
		SymbolTable: '/',
		SymbolCode:  'r',
		Comment:     "test",
	}
}
