// Package version exposes build-time identification injected by ldflags.
// goreleaser sets all three at release time; for `go run` / dev builds the
// defaults below apply.
package version

import "fmt"

var (
	// Version is the semver tag for this build, e.g. "0.1.0" (no leading "v").
	// "dev" means an unreleased local build.
	Version = "dev"
	// Commit is the short git SHA (or empty for dev builds).
	Commit = ""
	// BuildDate is the build timestamp in RFC3339 UTC (or empty for dev).
	BuildDate = ""
)

// String returns a human-readable one-liner: "0.1.0 (abcd1234, 2026-05-19T12:00:00Z)".
// Dev builds: "dev".
func String() string {
	if Version == "dev" && Commit == "" {
		return "dev"
	}
	if Commit == "" {
		return Version
	}
	if BuildDate == "" {
		return fmt.Sprintf("%s (%s)", Version, Commit)
	}
	return fmt.Sprintf("%s (%s, %s)", Version, Commit, BuildDate)
}
