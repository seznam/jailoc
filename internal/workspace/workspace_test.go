package workspace_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestResolveValidWorkspace(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/home/user/projects"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if resolved.Name != "default" {
		t.Fatalf("name mismatch: got %q", resolved.Name)
	}
	if resolved.Port != 4096 {
		t.Fatalf("port mismatch: got %d", resolved.Port)
	}
	if len(resolved.Paths) != 1 || resolved.Paths[0] != "/home/user/projects" {
		t.Fatalf("paths mismatch: got %#v", resolved.Paths)
	}
}

func TestResolveNonexistentWorkspace(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{},
	}

	_, err := workspace.Resolve(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent workspace")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveFromCWDMatches(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/home/user/projects"},
			},
		},
	}

	resolved, matchedPath, err := workspace.ResolveFromCWD(cfg, "/home/user/projects/foo")
	if err != nil {
		t.Fatalf("ResolveFromCWD returned error: %v", err)
	}
	if resolved.Name != "default" {
		t.Fatalf("workspace mismatch: got %q", resolved.Name)
	}
	if matchedPath != "/home/user/projects" {
		t.Fatalf("matched path mismatch: got %q", matchedPath)
	}
}

func TestResolveFromCWDNoMatch(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/home/user/projects"},
			},
		},
	}

	_, _, err := workspace.ResolveFromCWD(cfg, "/home/user/unrelated")
	if err == nil {
		t.Fatal("expected no match error")
	}
	if !strings.Contains(err.Error(), "no workspace matches") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPortAllocationAlphabetical(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"zebra": {},
			"alpha": {},
		},
	}

	if got := workspace.PortForWorkspace(cfg, "alpha"); got != 4096 {
		t.Fatalf("alpha port mismatch: got %d", got)
	}
	if got := workspace.PortForWorkspace(cfg, "zebra"); got != 4097 {
		t.Fatalf("zebra port mismatch: got %d", got)
	}
}

func TestPortAllocationSingleDefault(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {},
		},
	}

	if got := workspace.PortForWorkspace(cfg, "default"); got != 4096 {
		t.Fatalf("default port mismatch: got %d", got)
	}
}

func TestTildeExpansionInPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir failed: %v", err)
	}
	want, err := filepath.Abs(filepath.Join(home, "projects"))
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"~/projects"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved.Paths) != 1 || resolved.Paths[0] != want {
		t.Fatalf("tilde expansion mismatch: got %#v want %q", resolved.Paths, want)
	}
}

func TestPathWithSpaces(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir failed: %v", err)
	}
	want, err := filepath.Abs(filepath.Join(home, "My Projects", "foo"))
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"~/My Projects/foo"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if len(resolved.Paths) != 1 || resolved.Paths[0] != want {
		t.Fatalf("space path expansion mismatch: got %#v want %q", resolved.Paths, want)
	}
}

func TestBuildContextExpanded(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir failed: %v", err)
	}
	want, err := filepath.Abs(filepath.Join(home, "my-build"))
	if err != nil {
		t.Fatalf("Abs failed: %v", err)
	}

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				BuildContext: "~/my-build",
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.BuildContext != want {
		t.Fatalf("build context mismatch: got %q want %q", resolved.BuildContext, want)
	}
}

func TestBuildContextEmpty(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				BuildContext: "",
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.BuildContext != "" {
		t.Fatalf("expected empty build context, got %q", resolved.BuildContext)
	}
}

func TestCWDSubdirectoryMatches(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/home/user/projects"},
			},
		},
	}

	resolved, _, err := workspace.ResolveFromCWD(cfg, "/home/user/projects/deep/subdir")
	if err != nil {
		t.Fatalf("ResolveFromCWD returned error: %v", err)
	}
	if resolved.Name != "default" {
		t.Fatalf("workspace mismatch: got %q", resolved.Name)
	}
}

func TestCWDDoesNotMatchPrefix(t *testing.T) {
	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/home/user/projects"},
			},
		},
	}

	_, _, err := workspace.ResolveFromCWD(cfg, "/home/user/projectsother")
	if err == nil {
		t.Fatal("expected no match error for prefix-only path")
	}
}

func TestMatchesCWD(t *testing.T) {
	ws := &workspace.Resolved{Paths: []string{"/home/user/projects"}}
	if !workspace.MatchesCWD(ws, "/home/user/projects/x") {
		t.Fatal("expected subdirectory to match")
	}
	if workspace.MatchesCWD(ws, "/home/user/projectsother") {
		t.Fatal("expected prefix-only path not to match")
	}
	if !workspace.MatchesCWD(ws, "/home/user/projects") {
		t.Fatal("expected exact path to match")
	}
}
