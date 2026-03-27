package main

import (
	"os"
	"runtime/debug"

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
		}
	}

	if err := cmd.Execute(version, commit, date); err != nil {
		os.Exit(1)
	}
}
