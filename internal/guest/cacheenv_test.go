package guest

import (
	"slices"
	"testing"
)

func TestCacheEnv(t *testing.T) {
	t.Parallel()

	all := []string{
		"GOMODCACHE=/c/go-mod",
		"NPM_CONFIG_CACHE=/c/npm",
		"BUN_INSTALL_CACHE_DIR=/c/bun",
		"NUGET_PACKAGES=/c/nuget/packages",
		"NUGET_HTTP_CACHE_PATH=/c/nuget/v3-cache",
	}
	tests := []struct {
		name     string
		cacheDir string
		env      []string
		want     []string
	}{
		{name: "no cache dir", env: []string{"PATH=/bin"}},
		{name: "all unset", cacheDir: "/c", env: []string{"PATH=/bin"}, want: all},
		{name: "trailing slash", cacheDir: "/c/", want: all},
		{
			name:     "set by image",
			cacheDir: "/c",
			env:      []string{"GOMODCACHE=/go/pkg/mod", "NUGET_PACKAGES="},
			want:     []string{all[1], all[2], all[4]},
		},
		{
			name:     "alias set",
			cacheDir: "/c",
			env:      []string{"npm_config_cache=/npm"},
			want:     []string{all[0], all[2], all[3], all[4]},
		},
		{
			name:     "related variable does not count",
			cacheDir: "/c",
			env:      []string{"GOPATH=/go", "BUN_INSTALL=/opt/bun"},
			want:     all,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := cacheEnv(tt.cacheDir, tt.env); !slices.Equal(got, tt.want) {
				t.Fatalf("cacheEnv(%q, %q) = %q, want %q", tt.cacheDir, tt.env, got, tt.want)
			}
		})
	}
}
