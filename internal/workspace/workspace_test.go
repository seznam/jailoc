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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

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

func TestPortForWorkspaceUnknown(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"alpha": {},
			"beta":  {},
		},
	}

	got := workspace.PortForWorkspace(cfg, "unknown-workspace")
	if got != -1 {
		t.Fatalf("expected -1 for unknown workspace, got %d", got)
	}
}

func TestResolveNilConfig(t *testing.T) {
	t.Parallel()

	_, err := workspace.Resolve(nil, "somename")
	if err == nil {
		t.Fatal("expected error for nil config")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestMatchesCWDNilWorkspace(t *testing.T) {
	t.Parallel()

	got := workspace.MatchesCWD(nil, "/some/path")
	if got {
		t.Fatal("expected false for nil workspace")
	}
}

func TestDockerfileSet(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Dockerfile: "https://example.com/Dockerfile",
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Dockerfile != "https://example.com/Dockerfile" {
		t.Fatalf("dockerfile mismatch: got %q", resolved.Dockerfile)
	}
}

func TestDockerfileEmpty(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Dockerfile: "",
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if resolved.Dockerfile != "" {
		t.Fatalf("expected empty dockerfile, got %q", resolved.Dockerfile)
	}
}

func TestResolveEnvGlobalOnly(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Defaults: config.Defaults{
			Env: []string{"GLOBAL_A=1", "GLOBAL_B=2"},
		},
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/tmp"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if len(resolved.Env) != 2 {
		t.Fatalf("env length mismatch: got %#v", resolved.Env)
	}
	if resolved.Env[0] != "GLOBAL_A=1" || resolved.Env[1] != "GLOBAL_B=2" {
		t.Fatalf("env mismatch: got %#v", resolved.Env)
	}
}

func TestResolveEnvWorkspaceOnly(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/tmp"},
				Env:   []string{"WS_A=1", "WS_B=2"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	if len(resolved.Env) != 2 {
		t.Fatalf("env length mismatch: got %#v", resolved.Env)
	}
	if resolved.Env[0] != "WS_A=1" || resolved.Env[1] != "WS_B=2" {
		t.Fatalf("env mismatch: got %#v", resolved.Env)
	}
}

func TestResolveEnvMergeOverride(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		Defaults: config.Defaults{
			Env: []string{"SHARED=global", "GLOBAL_ONLY=1"},
		},
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/tmp"},
				Env:   []string{"SHARED=workspace", "WS_ONLY=1"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	want := []string{"SHARED=workspace", "GLOBAL_ONLY=1", "WS_ONLY=1"}
	if len(resolved.Env) != len(want) {
		t.Fatalf("env length mismatch: got %#v want %#v", resolved.Env, want)
	}
	for i := range want {
		if resolved.Env[i] != want[i] {
			t.Fatalf("env mismatch at %d: got %#v want %#v", i, resolved.Env, want)
		}
	}
}

func TestResolveEnvFile(t *testing.T) {
	t.Parallel()

	envFile, err := os.CreateTemp("", "jailoc-default-*.env")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(envFile.Name()) })

	if err := os.WriteFile(envFile.Name(), []byte("FROM_FILE=ok\nOTHER=two\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg := &config.Config{
		Defaults: config.Defaults{
			EnvFile: []string{envFile.Name()},
		},
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths: []string{"/tmp"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	want := []string{"FROM_FILE=ok", "OTHER=two"}
	if len(resolved.Env) != len(want) {
		t.Fatalf("env length mismatch: got %#v want %#v", resolved.Env, want)
	}
	for i := range want {
		if resolved.Env[i] != want[i] {
			t.Fatalf("env mismatch at %d: got %#v want %#v", i, resolved.Env, want)
		}
	}
}

func TestResolveEnvFullPipeline(t *testing.T) {
	t.Parallel()

	globalFile, err := os.CreateTemp("", "jailoc-global-*.env")
	if err != nil {
		t.Fatalf("CreateTemp global env failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(globalFile.Name()) })

	workspaceFile, err := os.CreateTemp("", "jailoc-workspace-*.env")
	if err != nil {
		t.Fatalf("CreateTemp workspace env failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(workspaceFile.Name()) })

	if err := os.WriteFile(globalFile.Name(), []byte("B=global-file\nC=global-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile global env failed: %v", err)
	}
	if err := os.WriteFile(workspaceFile.Name(), []byte("C=workspace-file\nD=workspace-file\n"), 0o600); err != nil {
		t.Fatalf("WriteFile workspace env failed: %v", err)
	}

	cfg := &config.Config{
		Defaults: config.Defaults{
			Env:     []string{"A=global", "B=global-inline"},
			EnvFile: []string{globalFile.Name()},
		},
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths:   []string{"/tmp"},
				EnvFile: []string{workspaceFile.Name()},
				Env:     []string{"D=workspace-inline", "A=workspace-inline"},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	want := []string{"A=workspace-inline", "B=global-file", "C=workspace-file", "D=workspace-inline"}
	if len(resolved.Env) != len(want) {
		t.Fatalf("env length mismatch: got %#v want %#v", resolved.Env, want)
	}
	for i := range want {
		if resolved.Env[i] != want[i] {
			t.Fatalf("env mismatch at %d: got %#v want %#v", i, resolved.Env, want)
		}
	}
}

func TestResolveEnvFileError(t *testing.T) {
	t.Parallel()

	missingPath := filepath.Join("/tmp", "jailoc-missing-env-file-do-not-create.env")

	cfg := &config.Config{
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths:   []string{"/tmp"},
				EnvFile: []string{missingPath},
			},
		},
	}

	_, err := workspace.Resolve(cfg, "default")
	if err == nil {
		t.Fatal("expected error for missing env_file")
	}
	if !strings.Contains(err.Error(), "resolving env for workspace default") {
		t.Fatalf("unexpected error context: %v", err)
	}
	if !strings.Contains(err.Error(), missingPath) {
		t.Fatalf("expected missing path in error, got: %v", err)
	}
}

func TestResolveEnvFileDedupPaths(t *testing.T) {
	t.Parallel()

	envFile, err := os.CreateTemp("", "jailoc-shared-*.env")
	if err != nil {
		t.Fatalf("CreateTemp failed: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(envFile.Name()) })

	if err := os.WriteFile(envFile.Name(), []byte("SHARED=value\n"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cfg := &config.Config{
		Defaults: config.Defaults{
			EnvFile: []string{envFile.Name()},
		},
		Workspaces: map[string]config.Workspace{
			"default": {
				Paths:   []string{"/tmp"},
				EnvFile: []string{envFile.Name()},
			},
		},
	}

	resolved, err := workspace.Resolve(cfg, "default")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}

	want := []string{"SHARED=value"}
	if len(resolved.Env) != len(want) {
		t.Fatalf("env length mismatch: got %#v want %#v", resolved.Env, want)
	}
	for i := range want {
		if resolved.Env[i] != want[i] {
			t.Fatalf("env mismatch at %d: got %#v want %#v", i, resolved.Env, want)
		}
	}
}
