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

func TestProgramLastWins(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{name: "empty"},
		{
			name: "lowercase overrides injected uppercase",
			env:  []string{"PATH=/bin", "NPM_CONFIG_CACHE=/c/npm", "HOME=/h", "npm_config_cache=/ws/npm"},
			want: []string{"PATH=/bin", "HOME=/h", "npm_config_cache=/ws/npm"},
		},
		{
			name: "uppercase overrides earlier lowercase",
			env:  []string{"npm_config_cache=/a", "NPM_CONFIG_CACHE=/b"},
			want: []string{"NPM_CONFIG_CACHE=/b"},
		},
		{
			name: "mixed sequence keeps only the last of the group",
			env:  []string{"NPM_CONFIG_CACHE=/1", "npm_config_cache=/2", "NPM_CONFIG_CACHE=/3", "npm_config_cache=/4", "X=1"},
			want: []string{"npm_config_cache=/4", "X=1"},
		},
		{
			name: "later empty value wins",
			env:  []string{"NPM_CONFIG_CACHE=/c/npm", "npm_config_cache="},
			want: []string{"npm_config_cache="},
		},
		{
			name: "single spelling is untouched",
			env:  []string{"NPM_CONFIG_CACHE=/c/npm", "GOMODCACHE=/c/go-mod"},
			want: []string{"NPM_CONFIG_CACHE=/c/npm", "GOMODCACHE=/c/go-mod"},
		},
		{
			name: "unrelated variables keep exact-name semantics",
			env:  []string{"A=1", "a=2", "A=3", "Npm_Config_Cache=/x", "NPM_CONFIG_CACHE=/y", "GOMODCACHE=/1", "gomodcache=/2", "B"},
			want: []string{"a=2", "A=3", "Npm_Config_Cache=/x", "NPM_CONFIG_CACHE=/y", "GOMODCACHE=/1", "gomodcache=/2", "B"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := programLastWins(tt.env); !slices.Equal(got, tt.want) {
				t.Fatalf("programLastWins(%q) = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}
