// Package paths decides where emcomm-objects keeps its config, objects file
// and log. The rules mirror VarMap so the two companion apps behave the same
// on a user's machine:
//
//   - EMCOMM_OBJECTS_DATA env var, when set, wins.
//   - An installed build (Inno Setup uninstaller beside the exe) keeps its
//     data in the user profile: %LOCALAPPDATA%\EmcommObjects on Windows,
//     os.UserConfigDir()/emcomm-objects elsewhere. It survives upgrades.
//   - A portable build or `go run` uses the directory the executable lives
//     in (or the working directory under `go run`), so a folder can be
//     unzipped anywhere and carried around.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppName is the folder name used in the user profile.
const AppName = "EmcommObjects"

// Mode describes how this binary was installed.
type Mode string

const (
	ModeInstalled Mode = "installed" // Inno Setup build (uninstaller beside exe)
	ModePortable  Mode = "portable"  // unzipped binary
	ModeSource    Mode = "source"    // go run / go build in a checkout
)

// ExeDir returns the directory containing the running executable, or "" if
// it cannot be determined.
func ExeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe)
}

// InstallMode inspects the executable's surroundings.
func InstallMode() Mode {
	dir := ExeDir()
	if dir == "" || isGoRunDir(dir) {
		return ModeSource
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ModePortable
	}
	for _, e := range entries {
		n := strings.ToLower(e.Name())
		if strings.HasPrefix(n, "unins") && strings.HasSuffix(n, ".exe") {
			return ModeInstalled
		}
	}
	return ModePortable
}

// isGoRunDir reports whether dir looks like Go's build cache (go run / go test).
func isGoRunDir(dir string) bool {
	d := filepath.ToSlash(strings.ToLower(dir))
	return strings.Contains(d, "/go-build") || strings.Contains(d, "/tmp/go-") || strings.Contains(d, "go-build")
}

// DataDir returns the directory for config, objects and logs (created on
// demand by callers that write).
func DataDir() string {
	if v := strings.TrimSpace(os.Getenv("EMCOMM_OBJECTS_DATA")); v != "" {
		return v
	}
	switch InstallMode() {
	case ModeInstalled:
		return profileDir()
	case ModePortable:
		return ExeDir()
	default:
		wd, err := os.Getwd()
		if err != nil {
			return "."
		}
		return wd
	}
}

func profileDir() string {
	if runtime.GOOS == "windows" {
		if base := os.Getenv("LOCALAPPDATA"); base != "" {
			return filepath.Join(base, AppName)
		}
	}
	if base, err := os.UserConfigDir(); err == nil && base != "" {
		return filepath.Join(base, "emcomm-objects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "."+strings.ToLower(AppName))
}

// Resolve returns p unchanged when absolute, otherwise joined onto dir.
func Resolve(dir, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

// Default file names inside DataDir.
const (
	ConfigFile  = "config.yaml"
	ObjectsFile = "objects.json"
	LogFile     = "emcomm-objects.log"
	UpdatesDir  = "updates"
)
