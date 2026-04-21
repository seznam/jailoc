package main

import (
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/cmd"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	// When installed via "go install", ldflags are not set and version stays "dev".
	// Fall back to build info embedded by the Go toolchain since 1.18.
	// See https://pkg.go.dev/runtime/debug#ReadBuildInfo
	if version == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok {
			if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
				version = bi.Main.Version
			}
			var modified bool
			for _, s := range bi.Settings {
				switch s.Key {
				case "vcs.revision":
					if len(s.Value) > 12 {
						commit = s.Value[:12]
					} else if s.Value != "" {
						commit = s.Value
					}
				case "vcs.time":
					date = s.Value
				case "vcs.modified":
					modified = s.Value == "true"
				}
			}
			if modified {
				commit += "-dirty"
			}

			// Remote installs (go install ...@branch) have no VCS settings
			// but embed a pseudo-version like v1.11.1-0.20260421130442-3758b6c5e57a.
			// Extract commit hash and timestamp from it.
			if commit == "none" {
				if rev := pseudoVersionRevision(version); rev != "" {
					commit = rev
				}
			}
			if date == "unknown" {
				if ts := pseudoVersionTime(version); ts != "" {
					date = ts
				}
			}
		}
	}

	if err := cmd.Execute(version, commit, date); err != nil {
		os.Exit(1)
	}
}

// pseudoVersionRevision extracts the 12-char commit hash from a Go module
// pseudo-version (e.g. "v1.11.1-0.20260421130442-3758b6c5e57a" → "3758b6c5e57a").
func pseudoVersionRevision(v string) string {
	i := strings.LastIndex(v, "-")
	if i < 0 {
		return ""
	}
	rev := v[i+1:]
	if len(rev) != 12 {
		return ""
	}
	for _, c := range rev {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return ""
		}
	}
	return rev
}

// pseudoVersionTime extracts the timestamp from a Go module pseudo-version
// (e.g. "v1.11.1-0.20260421130442-3758b6c5e57a" → "2026-04-21T13:04:42Z").
func pseudoVersionTime(v string) string {
	parts := strings.Split(v, "-")
	if len(parts) < 3 {
		return ""
	}
	ts := parts[len(parts)-2]
	if i := strings.LastIndex(ts, "."); i >= 0 {
		ts = ts[i+1:]
	}
	if len(ts) != 14 {
		return ""
	}
	t, err := time.Parse("20060102150405", ts)
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
