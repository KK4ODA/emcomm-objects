package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return New(filepath.Join(dir, "objects.json"))
}

func TestUpsertGetDelete(t *testing.T) {
	s := tempStore(t)
	o := Object{
		ObjectName:      "DCFR_1",
		SymbolTable:     "/",
		SymbolID:        "r",
		Latitude:        33.80,
		Longitude:       -84.33,
		IntervalMinutes: 30,
		Enabled:         true,
	}
	if err := s.Upsert(o); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("DCFR_1")
	if !ok || got.SymbolID != "r" {
		t.Fatalf("get: ok=%v got=%+v", ok, got)
	}

	// Update changes only the comment; preserve other fields.
	o2 := o
	o2.Comment = "updated"
	if err := s.Upsert(o2); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("DCFR_1"); got.Comment != "updated" {
		t.Errorf("comment: %q", got.Comment)
	}
	if len(s.List()) != 1 {
		t.Errorf("expected 1 object after update, got %d", len(s.List()))
	}

	if !s.Delete("DCFR_1") {
		t.Error("delete returned false")
	}
	if _, ok := s.Get("DCFR_1"); ok {
		t.Error("get after delete returned ok")
	}
	if s.Delete("never-existed") {
		t.Error("delete of non-existent returned true")
	}
}

func TestUpsertPreservesLastBeacon(t *testing.T) {
	s := tempStore(t)
	o := Object{
		ObjectName: "X", SymbolTable: "/", SymbolID: "r",
		Latitude: 0, Longitude: 0,
	}
	if err := s.Upsert(o); err != nil {
		t.Fatal(err)
	}
	beaconAt := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	s.MarkBeaconed("X", beaconAt)

	// Re-upsert without setting LastBeacon — should preserve.
	if err := s.Upsert(o); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("X")
	if !got.LastBeacon.Equal(beaconAt) {
		t.Errorf("LastBeacon: got %v, want %v", got.LastBeacon, beaconAt)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	objs := []Object{
		{
			ObjectName: "DCFR_1", ShowTooltip: true,
			SymbolTable: "/", SymbolID: "r",
			Comment: "DCFR Station 1", IntervalMinutes: 30,
			Latitude: 33.8011357, Longitude: -84.3301389,
			Enabled: true,
		},
		{
			ObjectName: "DCFR_2", ShowTooltip: true,
			SymbolTable: "/", SymbolID: "r",
			Comment: "DCFR Station 2", IntervalMinutes: 30,
			Latitude: 33.8604167, Longitude: -84.3350799,
			Enabled: true,
		},
	}
	for _, o := range objs {
		if err := s.Upsert(o); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	s2 := New(s.path)
	if err := s2.Load(); err != nil {
		t.Fatal(err)
	}
	got := s2.List()
	if len(got) != 2 {
		t.Fatalf("loaded %d, want 2", len(got))
	}
	if got[0].ObjectName != "DCFR_1" || got[0].Latitude != 33.8011357 {
		t.Errorf("loaded[0]: %+v", got[0])
	}
}

func TestLoadMissingFile(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err := s.Load(); err != nil {
		t.Errorf("expected nil for missing file, got %v", err)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("expected empty list, got %d", len(got))
	}
}

func TestLoadPinpointFormat(t *testing.T) {
	// Sample mimicking real Pinpoint output (clean JSON).
	js := `[
  {
    "ObjectName": "DCFR_1",
    "ShowTooltip": true,
    "SymbolTable": "/",
    "SymbolID": "r",
    "Comment": "DCFR Station 1",
    "IntervalMinutes": 30,
    "Latitude": 33.8011357,
    "Longitude": -84.3301389,
    "Altitude": 0.0,
    "LastBeacon": "0001-01-01T00:00:00"
  }
]`
	dir := t.TempDir()
	p := filepath.Join(dir, "in.json")
	if err := os.WriteFile(p, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	got := s.List()
	if len(got) != 1 || got[0].ObjectName != "DCFR_1" {
		t.Fatalf("loaded: %+v", got)
	}
	// Round-trip: save and reload.
	out := filepath.Join(dir, "out.json")
	s2 := New(out)
	for _, o := range got {
		s2.Upsert(o)
	}
	if err := s2.Save(); err != nil {
		t.Fatal(err)
	}
	s3 := New(out)
	if err := s3.Load(); err != nil {
		t.Fatal(err)
	}
	if len(s3.List()) != 1 {
		t.Errorf("re-loaded %d, want 1", len(s3.List()))
	}
}

func TestValidate(t *testing.T) {
	good := Object{ObjectName: "X", SymbolTable: "/", SymbolID: "r", Latitude: 0, Longitude: 0}
	if err := good.Validate(); err != nil {
		t.Errorf("expected good to validate: %v", err)
	}
	cases := []struct {
		mut  func(*Object)
		name string
	}{
		{func(o *Object) { o.ObjectName = "" }, "empty name"},
		{func(o *Object) { o.ObjectName = "TOOLONGOBJ" }, "long name"},
		{func(o *Object) { o.SymbolTable = "" }, "empty table"},
		{func(o *Object) { o.SymbolTable = "//" }, "2-char table"},
		{func(o *Object) { o.SymbolID = "" }, "empty symbol"},
		{func(o *Object) { o.Latitude = 91 }, "bad lat"},
		{func(o *Object) { o.Longitude = -181 }, "bad lon"},
		{func(o *Object) { o.IntervalMinutes = -1 }, "negative interval"},
	}
	for _, c := range cases {
		o := good
		c.mut(&o)
		if err := o.Validate(); err == nil {
			t.Errorf("%s: expected validation error", c.name)
		}
	}
}

func TestLoadStripsUTF8BOM(t *testing.T) {
	// PowerShell Out-File -Encoding utf8 and Windows Notepad prepend a BOM.
	js := "\xEF\xBB\xBF[{\"ObjectName\":\"X\",\"SymbolTable\":\"/\",\"SymbolID\":\"r\",\"Latitude\":0,\"Longitude\":0,\"IntervalMinutes\":30}]"
	dir := t.TempDir()
	p := filepath.Join(dir, "bom.json")
	if err := os.WriteFile(p, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(p)
	if err := s.Load(); err != nil {
		t.Fatalf("load with BOM: %v", err)
	}
	if got := s.List(); len(got) != 1 || got[0].ObjectName != "X" {
		t.Errorf("loaded: %+v", got)
	}
}

func TestMarkBeaconed(t *testing.T) {
	s := tempStore(t)
	s.Upsert(Object{ObjectName: "X", SymbolTable: "/", SymbolID: "r"})
	now := time.Now().UTC().Truncate(time.Second)
	if !s.MarkBeaconed("X", now) {
		t.Fatal("MarkBeaconed returned false for existing")
	}
	got, _ := s.Get("X")
	if !got.LastBeacon.Equal(now) {
		t.Errorf("LastBeacon: %v vs %v", got.LastBeacon, now)
	}
	if s.MarkBeaconed("nope", now) {
		t.Error("MarkBeaconed should return false for missing")
	}
}
