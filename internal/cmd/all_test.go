package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/workspace"
)

// Not parallel: targetWorkspaces reads the allFlag, workspaceFlag and
// workspaceExplicit globals.
func TestTargetWorkspaces(t *testing.T) {
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"beta": {}, "alpha": {}, "default": {},
	}}
	tests := []struct {
		name     string
		all      bool
		explicit string
		args     []string
		want     []string
		wantErr  string
	}{
		{name: "all in name order", all: true, want: []string{"alpha", "beta", "default"}},
		{name: "all with argument", all: true, args: []string{"alpha"}, wantErr: "--all cannot be combined"},
		{name: "all with --workspace", all: true, explicit: "alpha", wantErr: "--all cannot be combined"},
		{name: "argument", args: []string{"beta"}, want: []string{"beta"}},
		{name: "--workspace", explicit: "alpha", want: []string{"alpha"}},
		{name: "unknown", args: []string{"nope"}, wantErr: `resolve workspace "nope"`},
	}
	t.Cleanup(func() { allFlag, workspaceFlag, workspaceExplicit = false, "default", false })
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			allFlag = tc.all
			workspaceFlag, workspaceExplicit = "default", tc.explicit != ""
			if workspaceExplicit {
				workspaceFlag = tc.explicit
			}
			got, err := targetWorkspaces(cfg, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			names := make([]string, len(got))
			for i, ws := range got {
				names[i] = ws.Name
			}
			if strings.Join(names, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("workspaces = %v, want %v", names, tc.want)
			}
		})
	}
}

func TestForEachWorkspaceContinuesAfterFailure(t *testing.T) {
	t.Parallel()
	wss := []*workspace.Resolved{{Name: "a"}, {Name: "b"}, {Name: "c"}}
	var visited []string
	err := forEachWorkspace(context.Background(), wss, func(ws *workspace.Resolved) error {
		visited = append(visited, ws.Name)
		if ws.Name != "b" {
			return errors.New("boom")
		}
		return nil
	})
	if strings.Join(visited, ",") != "a,b,c" {
		t.Fatalf("visited = %v", visited)
	}
	if err == nil || !strings.Contains(err.Error(), `workspace "a": boom`) || !strings.Contains(err.Error(), `workspace "c": boom`) {
		t.Fatalf("error = %v", err)
	}
}

func TestForEachWorkspaceStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	wss := []*workspace.Resolved{{Name: "a"}, {Name: "b"}}
	var visited []string
	err := forEachWorkspace(ctx, wss, func(ws *workspace.Resolved) error {
		visited = append(visited, ws.Name)
		cancel()
		return nil
	})
	if len(visited) != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("visited = %v, error = %v", visited, err)
	}
}
