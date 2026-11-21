package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/workspace"
)

// resolvedTmpLabel describes the /tmp settings of a resolved workspace.
func resolvedTmpLabel(ws *workspace.Resolved) string {
	cleanup := "no cleanup"
	if ws.TmpTTL > 0 {
		cleanup = "cleanup after " + tmpDuration(ws.TmpTTL)
	}
	return fmt.Sprintf("%s, %s, %s", ws.TmpStorage, tmpSizeLabel(ws.TmpSizeBytes), cleanup)
}

// tmpSizeLabel formats a size in the largest binary unit that holds it
// exactly, so the configured value reads back as written (1 GiB, 512 MiB).
func tmpSizeLabel(n int64) string {
	for _, u := range []struct {
		size int64
		name string
	}{{1 << 30, "GiB"}, {1 << 20, "MiB"}, {1 << 10, "KiB"}} {
		if n >= u.size && n%u.size == 0 {
			return fmt.Sprintf("%d %s", n/u.size, u.name)
		}
	}
	return fmt.Sprintf("%d B", n)
}

// tmpDuration formats d without zero trailing units: 8h, 15m, 1h30m.
func tmpDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
