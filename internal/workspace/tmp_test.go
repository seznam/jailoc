package workspace_test

import (
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestResolveTmp(t *testing.T) {
	t.Parallel()

	str := func(v string) *string { return &v }

	tests := []struct {
		name     string
		defaults config.Defaults
		ws       config.Workspace
		storage  string
		size     int64
		ttl      time.Duration
		errSubst string
	}{
		{name: "host defaults", storage: "ram", size: 1 << 30, ttl: 8 * time.Hour},
		{
			name:     "defaults used when workspace unset",
			defaults: config.Defaults{TmpStorage: str("disk"), TmpSize: str("2g"), TmpTTL: str("15m")},
			storage:  "disk", size: 2 << 30, ttl: 15 * time.Minute,
		},
		{
			name:     "workspace overrides defaults",
			defaults: config.Defaults{TmpStorage: str("disk"), TmpSize: str("2g"), TmpTTL: str("15m")},
			ws:       config.Workspace{TmpStorage: str("ram"), TmpSize: str("64m"), TmpTTL: str("0")},
			storage:  "ram", size: 64 << 20, ttl: 0,
		},
		{
			name:     "settings merge independently",
			defaults: config.Defaults{TmpSize: str("2g")},
			ws:       config.Workspace{TmpTTL: str("1h")},
			storage:  "ram", size: 2 << 30, ttl: time.Hour,
		},
		{
			name:     "invalid size is rejected",
			ws:       config.Workspace{TmpSize: str("lots")},
			errSubst: "tmp_size",
		},
		{
			name:     "disk below minimum is rejected",
			ws:       config.Workspace{TmpStorage: str("disk"), TmpSize: str("1m")},
			errSubst: "16 MiB",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.ws.Paths = []string{"/data"}
			cfg := &config.Config{
				Defaults:   tt.defaults,
				Workspaces: map[string]config.Workspace{"test": tt.ws},
			}
			got, err := workspace.Resolve(cfg, "test")
			if tt.errSubst != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errSubst) {
					t.Fatalf("Resolve error = %v, want substring %q", err, tt.errSubst)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve failed: %v", err)
			}
			if got.TmpStorage != tt.storage || got.TmpSizeBytes != tt.size || got.TmpTTL != tt.ttl {
				t.Errorf("tmp = %q %d %v, want %q %d %v", got.TmpStorage, got.TmpSizeBytes, got.TmpTTL, tt.storage, tt.size, tt.ttl)
			}
		})
	}
}
