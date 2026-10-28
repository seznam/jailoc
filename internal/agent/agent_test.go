package agent

import (
	"slices"
	"strings"
	"testing"
)

func TestLookup(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"opencode", "claude", "copilot", "omo"} {
		h, ok := Lookup(name)
		if !ok || h.Name != name {
			t.Fatalf("Lookup(%q) = %+v, %v", name, h, ok)
		}
	}
	if _, ok := Lookup("vim"); ok {
		t.Fatal("Lookup(vim) found a harness")
	}
	if _, ok := Lookup(Default); !ok {
		t.Fatalf("default harness %q is not built in", Default)
	}
}

func TestHarnessDefinitions(t *testing.T) {
	t.Parallel()

	stateNames := map[string]string{"cache": "jailoc"}
	for _, h := range All() {
		t.Run(h.Name, func(t *testing.T) {
			if h.Binary == "" || h.Package == "" || h.Version == "" || h.Continue == "" || h.Resume == "" {
				t.Fatalf("incomplete harness: %+v", h)
			}
			if strings.HasPrefix(h.Version, "v") {
				t.Fatalf("version %q must be an npm version without v", h.Version)
			}
			if len(h.State) == 0 {
				t.Fatal("harness persists no state")
			}
			for _, s := range h.State {
				if owner, ok := stateNames[s.Name]; ok {
					t.Fatalf("state dir %q of %s already used by %s", s.Name, h.Name, owner)
				}
				stateNames[s.Name] = h.Name
				if !strings.HasPrefix(s.Path, "/home/agent/") {
					t.Fatalf("state dir %q outside the agent home", s.Path)
				}
			}
			for _, kv := range h.Env {
				if k, _, ok := strings.Cut(kv, "="); !ok || k == "" {
					t.Fatalf("invalid env entry %q", kv)
				}
			}
		})
	}
}

func TestCommand(t *testing.T) {
	t.Parallel()

	claude, _ := Lookup("claude")
	opencode, _ := Lookup("opencode")
	tests := []struct {
		name string
		h    Harness
		args []string
		id   string
		cont bool
		want []string
	}{
		{"opencode plain", opencode, nil, "", false, []string{"opencode"}},
		{"opencode session", opencode, nil, "ses_1", false, []string{"opencode", "--session", "ses_1"}},
		{"opencode continue", opencode, nil, "", true, []string{"opencode", "--continue"}},
		{"claude plain", claude, nil, "", false, []string{"claude", "--dangerously-skip-permissions"}},
		{"claude resume with args", claude, []string{"--model", "opus"}, "abc", false, []string{"claude", "--dangerously-skip-permissions", "--resume", "abc", "--model", "opus"}},
		{"claude continue", claude, nil, "", true, []string{"claude", "--dangerously-skip-permissions", "--continue"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.h.Command(tt.args, tt.id, tt.cont); !slices.Equal(got, tt.want) {
				t.Fatalf("Command() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultMounts(t *testing.T) {
	t.Parallel()

	opencode, _ := Lookup("opencode")
	claude, _ := Lookup("claude")
	if got := claude.DefaultMounts(); !slices.Equal(got, []string{"~/.agents:/home/agent/.agents:ro"}) {
		t.Fatalf("claude mounts = %q", got)
	}
	got := opencode.DefaultMounts()
	if !slices.Contains(got, "~/.config/opencode:/home/agent/.config/opencode:rw") || !slices.Contains(got, "~/.agents:/home/agent/.agents:ro") {
		t.Fatalf("opencode mounts = %q", got)
	}
	got[0] = "changed"
	if opencode.DefaultMounts()[0] == "changed" {
		t.Fatal("DefaultMounts shares its backing array")
	}
}
