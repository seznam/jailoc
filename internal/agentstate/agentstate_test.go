package agentstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
)

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func TestStatusLineCommands(t *testing.T) {
	t.Parallel()
	if got, want := ScriptStatusLineCommand("claude"), "node /etc/jailoc-tui-plugin/statusline.mjs claude"; got != want {
		t.Errorf("ScriptStatusLineCommand = %q, want %q", got, want)
	}
	for harness, want := range map[string]string{
		"claude":  ScriptStatusLineCommand("claude"),
		"copilot": WorkspaceStatusLineCommand,
		"agy":     WorkspaceStatusLineCommand,
	} {
		if got := StatusLineCommand(harness); got != want {
			t.Errorf("StatusLineCommand(%s) = %q, want %q", harness, got, want)
		}
		line := StatusLine(harness)
		if line["type"] != "command" || line["command"] != want || len(line) != 2 {
			t.Errorf("StatusLine(%s) = %v", harness, line)
		}
	}
}

func TestWorkspaceStatusLineCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		env  []string
		want string
	}{
		{[]string{"JAILOC_WORKSPACE=api"}, "\U0001F512 jailoc / api\n"},
		{[]string{}, "\U0001F512 jailoc / unknown\n"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("/bin/sh", "-c", WorkspaceStatusLineCommand) //nolint:gosec // G204: constant command
			cmd.Env = tt.env
			out, err := cmd.Output()
			if err != nil || string(out) != tt.want {
				t.Fatalf("status line = %q, %v; want %q", out, err, tt.want)
			}
		})
	}
}

func TestDirPrepJSON(t *testing.T) {
	t.Parallel()
	data, err := json.Marshal(DirPrep{Name: "claude", Path: "/home/agent/.claude"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), `{"name":"claude","path":"/home/agent/.claude"}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}

func TestRemoveConfigMountPoints(t *testing.T) {
	t.Parallel()
	h, _ := agent.Lookup("claude")
	data := t.TempDir()
	state := filepath.Join(data, "claude")
	for _, d := range []string{filepath.Join(state, "agents"), filepath.Join(state, "skills", "x"), filepath.Join(state, "projects")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"settings.json": "", "CLAUDE.md": "# notes", ".claude.json": ""} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveConfigMountPoints(openRoot(t, data), "claude", h.State[0], h); err != nil {
		t.Fatalf("RemoveConfigMountPoints: %v", err)
	}
	for name, want := range map[string]bool{"settings.json": false, "agents": false, "CLAUDE.md": true, "skills": true, "projects": true, ".claude.json": true} {
		if _, err := os.Lstat(filepath.Join(state, name)); (err == nil) != want {
			t.Errorf("%s exists = %v, want %v", name, err == nil, want)
		}
	}
}

func TestAddStateDefaults(t *testing.T) {
	t.Parallel()
	h, _ := agent.Lookup("claude")
	// tmp is what is left at the temporary path: "file" a stale file, "dir"
	// a directory that cannot be replaced, so the write fails.
	for name, tc := range map[string]struct {
		in, tmp, want string
		wantErrs      int
	}{
		"missing file":    {want: `{"bypassPermissionsModeAccepted":true}`},
		"other keys":      {in: `{"userID":"u","projects":{"/p":{"a":1}}}`, want: `{"bypassPermissionsModeAccepted":true,"projects":{"/p":{"a":1}},"userID":"u"}`},
		"set false":       {in: `{"bypassPermissionsModeAccepted":false}`, want: `{"bypassPermissionsModeAccepted":false}`},
		"null":            {in: `null`, want: `{"bypassPermissionsModeAccepted":true}`},
		"invalid":         {in: `{`, want: `{`, wantErrs: 1},
		"stale temp file": {in: `{"userID":"u"}`, tmp: "file", want: `{"bypassPermissionsModeAccepted":true,"userID":"u"}`},
		"failed write":    {in: `{"userID":"u"}`, tmp: "dir", want: `{"userID":"u"}`, wantErrs: 1},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			file := filepath.Join(data, "claude", ".claude.json")
			tmp := file + ".jailoc-tmp"
			if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
				t.Fatal(err)
			}
			if tc.in != "" {
				if err := os.WriteFile(file, []byte(tc.in), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch tc.tmp {
			case "file":
				if err := os.WriteFile(tmp, []byte("junk"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "dir":
				if err := os.MkdirAll(filepath.Join(tmp, "x"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			errs := AddStateDefaults(openRoot(t, data), "claude", h.State[0], h)
			if len(errs) != tc.wantErrs {
				t.Fatalf("AddStateDefaults errors = %v, want %d", errs, tc.wantErrs)
			}
			for _, err := range errs {
				if !strings.HasPrefix(err.Error(), "could not set bypassPermissionsModeAccepted in ") {
					t.Errorf("error = %q", err)
				}
			}
			got, err := os.ReadFile(file) //nolint:gosec // G304: test temp file
			if err != nil {
				t.Fatal(err)
			}
			var b bytes.Buffer
			if json.Compact(&b, got) != nil {
				b.Reset()
				b.Write(got)
			}
			if b.String() != tc.want {
				t.Errorf(".claude.json = %s, want %s", b.String(), tc.want)
			}
			if _, err := os.Lstat(tmp); tc.tmp != "dir" && !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temporary file left behind: %v", err)
			}
		})
	}
}

func TestAddStatusLine(t *testing.T) {
	t.Parallel()
	copilot, _ := agent.Lookup("copilot")
	claude, _ := agent.Lookup("claude")

	tests := []struct {
		name     string
		in       string
		wantCmd  string
		wantTok  any
		wantKeep string
	}{
		{name: "missing file", wantCmd: WorkspaceStatusLineCommand, wantTok: true},
		{name: "other keys kept", in: `{"model":"m"}`, wantCmd: WorkspaceStatusLineCommand, wantTok: true, wantKeep: "m"},
		{name: "user status line kept", in: `{"statusLine":{"type":"command","command":"mine"},"storeTokenPlaintext":false}`, wantCmd: "mine", wantTok: false},
		{name: "token added next to user status line", in: `{"statusLine":{"type":"command","command":"mine"}}`, wantCmd: "mine", wantTok: true},
		{name: "legacy script replaced", in: `{"statusLine":{"type":"command","command":"` + ScriptStatusLineCommand("copilot") + `"}}`, wantCmd: WorkspaceStatusLineCommand, wantTok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			data := t.TempDir()
			file := filepath.Join(data, "settings.json")
			if tt.in != "" {
				if err := os.WriteFile(file, []byte(tt.in), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := AddStatusLine(openRoot(t, data), ".", copilot); err != nil {
				t.Fatalf("AddStatusLine: %v", err)
			}
			var got map[string]any
			if b, err := os.ReadFile(file); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
				t.Fatalf("read settings: %q %v", b, err)
			}
			line, _ := got["statusLine"].(map[string]any)
			if line["command"] != tt.wantCmd || got["storeTokenPlaintext"] != tt.wantTok {
				t.Errorf("settings = %v", got)
			}
			if tt.wantKeep != "" && got["model"] != tt.wantKeep {
				t.Errorf("model = %v, want %q", got["model"], tt.wantKeep)
			}
		})
	}

	t.Run("agent without settings file", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		if err := AddStatusLine(openRoot(t, data), ".", claude); err != nil {
			t.Fatalf("AddStatusLine: %v", err)
		}
		entries, err := os.ReadDir(data)
		if err != nil || len(entries) != 0 {
			t.Fatalf("directory = %v, %v; want empty", entries, err)
		}
	})

	t.Run("settings replaced, not rewritten in place", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		file := filepath.Join(data, "settings.json")
		const old = `{"model":"m"}`
		if err := os.WriteFile(file, []byte(old), 0o600); err != nil {
			t.Fatal(err)
		}
		// A hard link keeps the old inode: an in-place write would change
		// its content, a replacement by rename leaves it as it was.
		link := filepath.Join(data, "old.json")
		if err := os.Link(file, link); err != nil {
			t.Fatal(err)
		}
		if err := AddStatusLine(openRoot(t, data), ".", copilot); err != nil {
			t.Fatalf("AddStatusLine: %v", err)
		}
		if b, err := os.ReadFile(link); err != nil || string(b) != old { //nolint:gosec // G304: test file
			t.Errorf("old inode = %q, %v; want %q", b, err, old)
		}
		if _, err := os.Lstat(file + ".jailoc-tmp"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("temporary file left behind: %v", err)
		}
	})

	t.Run("invalid settings", func(t *testing.T) {
		t.Parallel()
		data := t.TempDir()
		if err := os.WriteFile(filepath.Join(data, "settings.json"), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := AddStatusLine(openRoot(t, data), ".", copilot)
		if err == nil || !strings.Contains(err.Error(), "add the jailoc status line to the "+copilot.Title+" settings") {
			t.Fatalf("AddStatusLine error = %v", err)
		}
	})
}
