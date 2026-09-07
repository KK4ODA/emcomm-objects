package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kk4oda/emcomm-objects/internal/paths"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"0.2.0", "0.1.9", true},
		{"v0.2.0", "0.2.0", false},
		{"0.2.0", "0.10.0", false},
		{"1.0.0", "dev", true},
		{"0.3.1-beta.1", "0.3.0", true},
		{"0.3.0", "0.3.0-rc1", false},
	}
	for _, c := range cases {
		if got := IsNewer(c.latest, c.current); got != c.want {
			t.Errorf("IsNewer(%q,%q)=%v want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestPickAsset(t *testing.T) {
	assets := []Asset{
		{Name: "EmcommObjects-Setup-0.2.0.exe"},
		{Name: "emcomm-objects-0.2.0-windows-x64-portable.zip"},
		{Name: "emcomm-objects-0.2.0-macos-arm64.tar.gz"},
		{Name: "emcomm-objects-0.2.0-macos-x64.tar.gz"},
		{Name: "emcomm-objects-0.2.0-linux-x64.tar.gz"},
		{Name: "emcomm-objects-0.2.0-linux-arm64.tar.gz"},
		{Name: "SHA256SUMS.txt"},
	}
	check := func(goos, arch string, mode paths.Mode, want string) {
		t.Helper()
		a := PickAsset(assets, goos, arch, mode)
		if a == nil || a.Name != want {
			t.Errorf("%s/%s/%s: got %+v want %s", goos, arch, mode, a, want)
		}
	}
	check("windows", "amd64", paths.ModeInstalled, "EmcommObjects-Setup-0.2.0.exe")
	check("windows", "amd64", paths.ModePortable, "emcomm-objects-0.2.0-windows-x64-portable.zip")
	check("darwin", "arm64", paths.ModePortable, "emcomm-objects-0.2.0-macos-arm64.tar.gz")
	check("darwin", "amd64", paths.ModePortable, "emcomm-objects-0.2.0-macos-x64.tar.gz")
	check("linux", "amd64", paths.ModeSource, "emcomm-objects-0.2.0-linux-x64.tar.gz")
	check("linux", "arm64", paths.ModeSource, "emcomm-objects-0.2.0-linux-arm64.tar.gz")
	if PickAsset(nil, "linux", "amd64", paths.ModeSource) != nil {
		t.Error("no assets must yield nil")
	}
}

func TestFindSum(t *testing.T) {
	sums := "abc123  EmcommObjects-Setup-0.2.0.exe\ndef456 *emcomm-objects-0.2.0-linux-x64.tar.gz\n"
	if FindSum(sums, "emcomm-objects-0.2.0-linux-x64.tar.gz") != "def456" {
		t.Fatal("binary-mode marker not handled")
	}
	if FindSum(sums, "nope") != "" {
		t.Fatal("unknown file must be empty")
	}
}

func TestCheckAndSnapshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"tag_name":"v0.9.0","body":"notes","html_url":"https://x/rel","assets":[{"name":"emcomm-objects-0.9.0-linux-x64.tar.gz","browser_download_url":"https://x/a","size":10},{"name":"SHA256SUMS.txt","browser_download_url":"https://x/s"}]}`))
	}))
	defer srv.Close()
	old := apiLatest
	apiLatest = srv.URL
	defer func() { apiLatest = old }()

	skip := ""
	settings := Settings{Check: true, IntervalHours: 12}
	u := New("0.1.0", paths.ModeSource, t.TempDir(), func() Settings { s := settings; s.SkipVersion = skip; return s },
		func(v string) error { skip = v; return nil }, nil)
	s := u.Check(context.Background())
	if s.Error != "" || s.Latest != "0.9.0" || !s.Available || s.URL != "https://x/rel" {
		t.Fatalf("state: %+v", s)
	}
	if err := u.Skip("0.9.0"); err != nil {
		t.Fatal(err)
	}
	if u.Snapshot().Available {
		t.Fatal("skipped version still reported available")
	}
	if u.Snapshot().CanSelfUpdate {
		t.Fatal("source build must not self-update")
	}
	if err := u.Apply(context.Background()); err == nil {
		t.Fatal("apply must refuse on a source build")
	}
}

func TestCheckFailureIsRecorded(t *testing.T) {
	old := apiLatest
	apiLatest = "http://127.0.0.1:1/nope"
	defer func() { apiLatest = old }()
	u := New("0.1.0", paths.ModeSource, t.TempDir(), func() Settings { return Settings{Check: true} }, nil, nil)
	if s := u.Check(context.Background()); s.Error == "" || s.Available {
		t.Fatalf("expected recorded error: %+v", s)
	}
}
