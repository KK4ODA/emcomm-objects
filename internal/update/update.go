// Package update checks GitHub for a newer release and, on Windows, can
// install it: the Inno Setup installer for installed builds, an in-place
// exe swap for the portable zip. macOS/Linux/source builds get the download
// link. Mirrors VarMap's updater so the two companion apps behave alike.
//
// Check: GET https://api.github.com/repos/KK4ODA/emcomm-objects/releases/latest
// (unauthenticated; only the version number is fetched, nothing about the
// user is sent). Apply: download the asset into <data>/updates, verify size
// and SHA-256 against the release's SHA256SUMS.txt, hand over to a small
// batch helper that waits for this process to exit, installs/swaps and
// restarts the app.
package update

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/paths"
)

// Repo is the GitHub repository releases are published to.
const Repo = "KK4ODA/emcomm-objects"

// ReleasesPage is the human landing page.
const ReleasesPage = "https://github.com/" + Repo + "/releases"

var apiLatest = "https://api.github.com/repos/" + Repo + "/releases/latest"

// Asset is a downloadable release file.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// State is what the UI shows.
type State struct {
	Current       string    `json:"current"`
	Mode          string    `json:"mode"` // installed | portable | source
	Latest        string    `json:"latest,omitempty"`
	Available     bool      `json:"available"`
	Notes         string    `json:"notes,omitempty"`
	URL           string    `json:"url"`
	Asset         *Asset    `json:"asset,omitempty"`
	SumsURL       string    `json:"-"`
	CheckedAt     time.Time `json:"checked_at,omitempty"`
	Error         string    `json:"error,omitempty"`
	Skipped       string    `json:"skipped,omitempty"`
	Applying      string    `json:"applying,omitempty"` // downloading | installing
	CanSelfUpdate bool      `json:"can_self_update"`
	Enabled       bool      `json:"enabled"`
}

// Settings is the slice of config the updater needs, supplied by a getter
// so Settings changes apply without a restart.
type Settings struct {
	Check         bool
	IntervalHours float64
	SkipVersion   string
}

// Updater owns the check loop and the apply logic.
type Updater struct {
	current  string
	mode     paths.Mode
	dataDir  string
	settings func() Settings
	onSkip   func(version string) error
	log      *slog.Logger
	client   *http.Client
	exit     func()

	mu    sync.Mutex
	state State
	wake  chan struct{}
}

// New builds an updater. settings must not be nil; onSkip persists the
// skipped version (may be nil); exit terminates the process after the
// helper is launched (defaults to os.Exit(0) after 2 s).
func New(current string, mode paths.Mode, dataDir string, settings func() Settings, onSkip func(string) error, log *slog.Logger) *Updater {
	if log == nil {
		log = slog.Default()
	}
	u := &Updater{
		current:  current,
		mode:     mode,
		dataDir:  dataDir,
		settings: settings,
		onSkip:   onSkip,
		log:      log,
		client:   &http.Client{Timeout: 30 * time.Second},
		wake:     make(chan struct{}, 1),
	}
	u.exit = func() {
		time.AfterFunc(2*time.Second, func() { os.Exit(0) })
	}
	u.state = State{
		Current:       current,
		Mode:          string(mode),
		URL:           ReleasesPage,
		CanSelfUpdate: runtime.GOOS == "windows" && (mode == paths.ModeInstalled || mode == paths.ModePortable),
	}
	return u
}

// Snapshot returns the current state, with Available computed against the
// running version and the skipped version.
func (u *Updater) Snapshot() State {
	u.mu.Lock()
	s := u.state
	u.mu.Unlock()
	st := u.settings()
	s.Enabled = st.Check
	s.Skipped = st.SkipVersion
	s.Available = u.current != "dev" && s.Latest != "" && IsNewer(s.Latest, u.current) && !sameVersion(s.Skipped, s.Latest)
	return s
}

// Wake triggers a check soon (Settings toggled the check on).
func (u *Updater) Wake() {
	select {
	case u.wake <- struct{}{}:
	default:
	}
}

// Run waits a little for the app to come up, cleans leftovers, then checks
// on the configured interval until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(15 * time.Second):
	}
	u.Cleanup()
	for {
		st := u.settings()
		if st.Check {
			u.Check(ctx)
		}
		hours := st.IntervalHours
		if hours < 1 {
			hours = 12
		}
		select {
		case <-ctx.Done():
			return
		case <-u.wake:
		case <-time.After(time.Duration(hours * float64(time.Hour))):
		}
	}
}

// Cleanup removes installers/helpers left by a previous update.
func (u *Updater) Cleanup() {
	dir := filepath.Join(u.dataDir, paths.UpdatesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if strings.HasSuffix(n, ".exe") || strings.HasSuffix(n, ".cmd") || strings.HasSuffix(n, ".zip") || strings.HasSuffix(n, ".tar.gz") {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	// A previous portable swap leaves the old binary beside the new one.
	if exe, err := os.Executable(); err == nil {
		_ = os.Remove(strings.TrimSuffix(exe, filepath.Ext(exe)) + ".old.exe")
	}
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
	Assets  []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

// Check queries GitHub once and records the result.
func (u *Updater) Check(ctx context.Context) State {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, apiLatest, nil)
	req.Header.Set("User-Agent", "emcomm-objects/"+u.current+" (+https://github.com/"+Repo+")")
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := u.client.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("GitHub responded %s", resp.Status)
		}
	}
	var rel ghRelease
	if err == nil {
		err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&rel)
	}
	u.mu.Lock()
	u.state.CheckedAt = time.Now().UTC()
	if err != nil {
		u.state.Error = err.Error()
		u.mu.Unlock()
		u.log.Debug("update check failed", "err", err)
		return u.Snapshot()
	}
	u.state.Error = ""
	u.state.Latest = strings.TrimPrefix(rel.TagName, "v")
	u.state.Notes = truncate(rel.Body, 4000)
	if rel.HTMLURL != "" {
		u.state.URL = rel.HTMLURL
	}
	var assets []Asset
	for _, a := range rel.Assets {
		assets = append(assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		if a.Name == "SHA256SUMS.txt" {
			u.state.SumsURL = a.URL
		}
	}
	u.state.Asset = PickAsset(assets, runtime.GOOS, runtime.GOARCH, u.mode)
	u.mu.Unlock()
	s := u.Snapshot()
	if s.Available {
		u.log.Info("update available", "latest", s.Latest, "running", u.current)
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Skip remembers a version the operator does not want to hear about.
func (u *Updater) Skip(version string) error {
	if u.onSkip == nil {
		return nil
	}
	return u.onSkip(version)
}

// PickAsset chooses the download for this machine from the release's assets.
func PickAsset(assets []Asset, goos, goarch string, mode paths.Mode) *Asset {
	arch := "x64"
	if goarch == "arm64" {
		arch = "arm64"
	}
	var want []string
	switch goos {
	case "windows":
		portable := "windows-" + arch + "-portable"
		if mode == paths.ModeInstalled {
			want = []string{"-Setup-", portable}
		} else {
			want = []string{portable, "-Setup-"}
		}
	case "darwin":
		want = []string{"macos-" + arch}
	default:
		want = []string{"linux-" + arch}
	}
	for _, key := range want {
		for i := range assets {
			if strings.Contains(assets[i].Name, key) {
				a := assets[i]
				return &a
			}
		}
	}
	return nil
}

var versionRe = regexp.MustCompile(`\d+`)

// ParseVersion turns "v0.2.10-beta" into [0 2 10]; a "dev" build is [0].
func ParseVersion(v string) []int {
	v = strings.SplitN(strings.TrimSpace(v), "-", 2)[0]
	var out []int
	for _, n := range versionRe.FindAllString(v, 4) {
		i, _ := strconv.Atoi(n)
		out = append(out, i)
	}
	if len(out) == 0 {
		return []int{0}
	}
	return out
}

// IsNewer reports whether latest is a higher version than current.
func IsNewer(latest, current string) bool {
	a, b := ParseVersion(latest), ParseVersion(current)
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			return x > y
		}
	}
	return false
}

func sameVersion(a, b string) bool {
	return a != "" && strings.TrimPrefix(a, "v") == strings.TrimPrefix(b, "v")
}

// Apply downloads and installs the available update (Windows only). On
// success the process exits shortly after and the helper restarts it.
func (u *Updater) Apply(ctx context.Context) error {
	s := u.Snapshot()
	if !s.CanSelfUpdate {
		return errors.New("self-update is only available for the Windows builds; use the download link")
	}
	if !s.Available || s.Asset == nil {
		return errors.New("no update available")
	}
	u.setApplying("downloading")
	err := u.apply(ctx, s)
	if err != nil {
		u.setApplying("")
		u.log.Warn("update failed", "err", err)
	}
	return err
}

func (u *Updater) setApplying(v string) {
	u.mu.Lock()
	u.state.Applying = v
	u.mu.Unlock()
}

func (u *Updater) apply(ctx context.Context, s State) error {
	dir := filepath.Join(u.dataDir, paths.UpdatesDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(dir, s.Asset.Name)
	sum, err := u.download(ctx, s.Asset.URL, dest)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(dest); err == nil && s.Asset.Size > 0 && fi.Size() != s.Asset.Size {
		return fmt.Errorf("download size mismatch (%d vs %d bytes)", fi.Size(), s.Asset.Size)
	}
	if s.SumsURL != "" {
		expected, err := u.expectedSum(ctx, s.SumsURL, s.Asset.Name)
		if err != nil {
			return err
		}
		if expected != "" && !strings.EqualFold(expected, sum) {
			_ = os.Remove(dest)
			return errors.New("SHA-256 checksum mismatch; download discarded")
		}
		u.log.Info("update checksum verified", "asset", s.Asset.Name)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u.setApplying("installing")
	var script string
	if strings.Contains(s.Asset.Name, "-Setup-") {
		script = installerScript(os.Getpid(), dest, exe)
	} else {
		newExe := filepath.Join(dir, "emcomm-objects.new.exe")
		if err := extractExe(dest, newExe); err != nil {
			return err
		}
		script = swapScript(os.Getpid(), newExe, exe)
	}
	helper := filepath.Join(dir, "apply_update.cmd")
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		return err
	}
	if err := launchDetached(helper); err != nil {
		return err
	}
	u.log.Info("update helper launched; exiting so it can install", "version", s.Latest)
	u.exit()
	return nil
}

func (u *Updater) download(ctx context.Context, url, dest string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "emcomm-objects/"+u.current)
	c := &http.Client{Timeout: 10 * time.Minute}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download: %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (u *Updater) expectedSum(ctx context.Context, url, name string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	req.Header.Set("User-Agent", "emcomm-objects/"+u.current)
	resp, err := u.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return FindSum(string(data), name), nil
}

// FindSum extracts the hex digest for name from sha256sum-style text.
func FindSum(sums, name string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			return fields[0]
		}
	}
	return ""
}

// extractExe pulls emcomm-objects.exe out of the portable zip.
func extractExe(zipPath, dest string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		if strings.EqualFold(filepath.Base(f.Name), "emcomm-objects.exe") {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			out, err := os.Create(dest)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.LimitReader(rc, 512<<20))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		}
	}
	return errors.New("emcomm-objects.exe not found in the portable zip")
}

// installerScript: wait for us to exit, run the Inno Setup installer
// silently, restart the app.
func installerScript(pid int, installer, exe string) string {
	return strings.Join([]string{
		"@echo off",
		"title Emcomm Objects update",
		"echo Waiting for Emcomm Objects to close...",
		":wait",
		fmt.Sprintf(`tasklist /FI "PID eq %d" 2>nul | find "%d" >nul && (timeout /t 1 /nobreak >nul & goto wait)`, pid, pid),
		"echo Installing update...",
		fmt.Sprintf(`"%s" /VERYSILENT /SUPPRESSMSGBOXES /NORESTART /CLOSEAPPLICATIONS /NOCANCEL`, installer),
		"if errorlevel 1 (echo Installer reported error %errorlevel%. & pause & exit /b 1)",
		fmt.Sprintf(`start "" "%s" --no-browser`, exe),
		`(goto) 2>nul & del "%~f0"`,
		"",
	}, "\r\n")
}

// swapScript: wait for us to exit, move the running exe aside (allowed on
// Windows), put the new one in place, restart.
func swapScript(pid int, newExe, exe string) string {
	old := strings.TrimSuffix(exe, filepath.Ext(exe)) + ".old.exe"
	return strings.Join([]string{
		"@echo off",
		"title Emcomm Objects update",
		"echo Waiting for Emcomm Objects to close...",
		":wait",
		fmt.Sprintf(`tasklist /FI "PID eq %d" 2>nul | find "%d" >nul && (timeout /t 1 /nobreak >nul & goto wait)`, pid, pid),
		fmt.Sprintf(`if exist "%s" del /f /q "%s"`, old, old),
		fmt.Sprintf(`move /y "%s" "%s" >nul || (echo Could not move the old executable. & pause & exit /b 1)`, exe, old),
		fmt.Sprintf(`move /y "%s" "%s" >nul || (move /y "%s" "%s" >nul & echo Could not install the new executable. & pause & exit /b 1)`, newExe, exe, old, exe),
		fmt.Sprintf(`start "" "%s" --no-browser`, exe),
		`(goto) 2>nul & del "%~f0"`,
		"",
	}, "\r\n")
}

func launchDetached(script string) error {
	if runtime.GOOS != "windows" {
		return errors.New("self-update helper is Windows-only")
	}
	cmd := exec.Command("cmd.exe", "/c", "start", "Emcomm Objects update", "/min", script)
	cmd.SysProcAttr = detachedAttr()
	return cmd.Start()
}
