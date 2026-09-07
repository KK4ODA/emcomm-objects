package paths

import (
	"path/filepath"
	"testing"
)

func TestResolve(t *testing.T) {
	abs := filepath.Join(t.TempDir(), "x.json")
	if got := Resolve("/data", abs); got != abs {
		t.Fatalf("absolute path changed: %s", got)
	}
	if got := Resolve("/data", "objects.json"); got != filepath.Join("/data", "objects.json") {
		t.Fatalf("relative not joined: %s", got)
	}
	if got := Resolve("/data", ""); got != "" {
		t.Fatalf("empty must stay empty: %q", got)
	}
}

func TestDataDirEnvOverride(t *testing.T) {
	t.Setenv("EMCOMM_OBJECTS_DATA", "/override")
	if got := DataDir(); got != "/override" {
		t.Fatalf("env override ignored: %s", got)
	}
}

func TestInstallModeUnderGoTest(t *testing.T) {
	// The test binary lives in Go's build cache, which must be treated as source.
	if m := InstallMode(); m != ModeSource {
		t.Fatalf("expected source mode under go test, got %s", m)
	}
}
