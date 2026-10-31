package cmd

import (
	"bytes"
	"context"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

func TestListWorkspaces(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"zz-list-test-b": {Paths: []string{home + "/b", "/srv/b"}},
		"zz-list-test-a": {},
	}}
	var out bytes.Buffer
	if err := listWorkspaces(context.Background(), &out, cfg, "zz-list-test-b", home); err != nil {
		t.Fatalf("listWorkspaces: %v", err)
	}
	want := "  WORKSPACE       AGENT     STATE    SESSIONS  PATHS\n" +
		"  zz-list-test-a  opencode  stopped  -         -\n" +
		"* zz-list-test-b  opencode  stopped  -         ~/b, /srv/b\n"
	if out.String() != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out.String(), want)
	}
}

func TestShortenHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path, home, want string
	}{
		{"/home/u", "/home/u", "~"},
		{"/home/u/src", "/home/u", "~/src"},
		{"/home/user/src", "/home/u", "/home/user/src"},
		{"/srv", "/home/u", "/srv"},
		{"/home/u/src", "", "/home/u/src"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := shortenHome(tt.path, tt.home); got != tt.want {
				t.Errorf("shortenHome(%q, %q) = %q, want %q", tt.path, tt.home, got, tt.want)
			}
		})
	}
}
