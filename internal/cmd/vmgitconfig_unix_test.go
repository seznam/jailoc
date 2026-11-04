//go:build darwin || linux

package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestRewriteGitConfig(t *testing.T) {
	t.Parallel()
	const home = "/Users/me"
	tests := []struct {
		name, in, want string
	}{
		{
			name: "gitdir under the home directory",
			in:   "[includeIf \"gitdir:~/work/\"]\n\tpath = ~/.gitconfig-work\n",
			want: "[includeIf \"gitdir:/Users/me/work/\"]\n\tpath = ~/.gitconfig-work\n",
		},
		{
			name: "case-insensitive gitdir",
			in:   "[IncludeIf \"gitdir/i:~/Work/\"]\n\tpath = x\n",
			want: "[IncludeIf \"gitdir/i:/Users/me/Work/\"]\n\tpath = x\n",
		},
		{
			name: "host home path value",
			in:   "[core]\n\texcludesFile = /Users/me/.gitignore # global\n",
			want: "[core]\n\texcludesFile = \"~/.gitignore\"\n",
		},
		{
			name: "variable after the section header",
			in:   "[include] path = /Users/me/x.inc\n",
			want: "[include] path = \"~/x.inc\"\n",
		},
		{
			name: "continued value",
			in:   "[user]\n\tsigningKey = /Users/me/.ssh/\\\nid.pub\n\tname = me\n",
			want: "[user]\n\tsigningKey = \"~/.ssh/id.pub\"\n\tname = me\n",
		},
		{
			name: "variable in a subsection",
			in:   "[gpg \"ssh\"]\n\tallowedSignersFile = /Users/me/.ssh/allowed_signers\n",
			want: "[gpg \"ssh\"]\n\tallowedSignersFile = \"~/.ssh/allowed_signers\"\n",
		},
		{
			name: "other values untouched",
			in:   "[core]\n\thooksPath = /Users/me/hooks\n\texcludesFile = /Users/meow/x\n[user]\n\tname = /Users/me/x\n[includeIf \"gitdir:/Users/me/src/\"]\n\tpath = rel.inc\n",
			want: "[core]\n\thooksPath = /Users/me/hooks\n\texcludesFile = /Users/meow/x\n[user]\n\tname = /Users/me/x\n[includeIf \"gitdir:/Users/me/src/\"]\n\tpath = rel.inc\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := string(rewriteGitConfig([]byte(tt.in), home)); got != tt.want {
				t.Errorf("rewriteGitConfig =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// gitConfigFiles writes files, relative to root, and returns root.
func gitConfigFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// collectGitConfigSnapshots runs gitConfigSnapshots and returns the snapshots
// as destination → "host" (or "host (generated)") and the warnings.
func collectGitConfigSnapshots(home, xdg string) (map[string]string, []string) {
	var warnings []string
	out := map[string]string{}
	for _, s := range gitConfigSnapshots(home, xdg, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}) {
		v := s.Host
		if s.data != nil {
			v += " (generated)"
		}
		out[s.Container] = v
	}
	return out, warnings
}

func sshPublicKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return string(ssh.MarshalAuthorizedKey(key))
}

func TestGitConfigSnapshots(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	outside := t.TempDir()
	gitConfigFiles(t, outside, map[string]string{"x.inc": "[user]\n"})
	gitConfigFiles(t, home, map[string]string{ //nolint:gosec // G101: test fixture
		".gitconfig": "[include]\n\tpath = ~/.config/git/extra.inc\n\tpath = work.inc\n" +
			"[includeIf \"gitdir:~/src/\"]\n\tpath = " + filepath.Join(outside, "x.inc") + "\n" +
			"[core]\n\texcludesFile = ~/.gitignore_global\n\tattributesFile = ~/missing-attributes\n" +
			"[gpg \"ssh\"]\n\tallowedSignersFile = ~/.ssh/allowed_signers\n" +
			"[user]\n\tsigningKey = " + filepath.Join(home, ".ssh", "id_ed25519.pub") + "\n" +
			"[commit]\n\ttemplate = ~/.ssh/template\n",
		".config/git/config":      "[user]\n\tname = me\n",
		".config/git/ignore":      "*.swp\n",
		".config/git/credentials": "https://user:token@example.com\n",
		".config/git/extra.inc":   "[core]\n\tpager = less\n",
		"work.inc":                "[include]\n\tpath = work.inc\n",
		".gitignore_global":       "*.o\n",
		".ssh/allowed_signers":    "me@example.com ssh-ed25519 AAAA\n",
		".ssh/id_ed25519.pub":     sshPublicKey(t),
		".ssh/template":           "subject\n",
	})
	got, warnings := collectGitConfigSnapshots(home, "")
	want := map[string]string{
		guestHome + "/.gitconfig":            filepath.Join(home, ".gitconfig") + " (generated)",
		guestHome + "/.config/git/extra.inc": filepath.Join(home, ".config/git/extra.inc") + " (generated)",
		guestHome + "/work.inc":              filepath.Join(home, "work.inc") + " (generated)",
		guestHome + "/.gitignore_global":     filepath.Join(home, ".gitignore_global"),
		guestHome + "/.ssh/allowed_signers":  filepath.Join(home, ".ssh/allowed_signers"),
		guestHome + "/.ssh/id_ed25519.pub":   filepath.Join(home, ".ssh/id_ed25519.pub"),
		guestHome + "/.config/git/config":    filepath.Join(home, ".config/git/config") + " (generated)",
		guestHome + "/.config/git/ignore":    filepath.Join(home, ".config/git/ignore"),
	}
	if !maps.Equal(got, want) {
		t.Errorf("snapshots = %v\nwant %v", got, want)
	}
	all := strings.Join(warnings, "\n")
	assertContains(t, all, "includeIf.path "+filepath.Join(outside, "x.inc")+" is outside the home directory")
	assertContains(t, all, "core.attributesFile "+filepath.Join(home, "missing-attributes")+" does not exist")
	assertContains(t, all, "commit.template "+filepath.Join(home, ".ssh/template")+" is in ~/.ssh")
	if len(warnings) != 3 {
		t.Errorf("warnings = %q, want 3", warnings)
	}
	for _, s := range gitConfigSnapshots(home, "", func(string, ...any) {}) {
		if s.Container == guestGitConfig {
			assertContains(t, string(s.data), "[includeIf \"gitdir:"+home+"/src/\"]")
			assertContains(t, string(s.data), "signingKey = \"~/.ssh/id_ed25519.pub\"")
		}
	}
}

func TestGitConfigSnapshotsSigningKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, value, wantWarning string
		copied                   bool
	}{
		{name: "public key", value: "~/.ssh/id.pub", copied: true},
		{name: "private key", value: "~/.ssh/id", wantWarning: "is not an SSH public key"},
		{name: "literal key", value: "key::ssh-ed25519 AAAA"},
		{name: "GPG key ID", value: "0123ABCD"},
		{name: "missing file", value: "~/.ssh/gone.pub", wantWarning: "does not exist"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			gitConfigFiles(t, home, map[string]string{
				".gitconfig":  "[user]\n\tsigningKey = " + tt.value + "\n",
				".ssh/id.pub": sshPublicKey(t),
				".ssh/id":     "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----\n",
			})
			got, warnings := collectGitConfigSnapshots(home, "")
			_, copied := got[guestHome+"/.ssh/id.pub"]
			if copied != tt.copied || len(got) != 1+map[bool]int{true: 1}[tt.copied] {
				t.Errorf("snapshots = %v, want public key copied = %v", got, tt.copied)
			}
			if _, ok := got[guestHome+"/.ssh/id"]; ok {
				t.Error("private key copied")
			}
			if tt.wantWarning == "" && len(warnings) != 0 {
				t.Errorf("warnings = %q, want none", warnings)
			}
			if tt.wantWarning != "" {
				assertContains(t, strings.Join(warnings, "\n"), tt.wantWarning)
			}
		})
	}
}

func TestGitConfigSnapshotsXDG(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	xdg := t.TempDir()
	gitConfigFiles(t, xdg, map[string]string{
		"git/config":     "[include]\n\tpath = local.inc\n",
		"git/local.inc":  "[user]\n",
		"git/attributes": "*.png binary\n",
	})
	got, warnings := collectGitConfigSnapshots(home, xdg)
	want := map[string]string{
		guestHome + "/.config/git/config":     filepath.Join(xdg, "git/config") + " (generated)",
		guestHome + "/.config/git/local.inc":  filepath.Join(xdg, "git/local.inc") + " (generated)",
		guestHome + "/.config/git/attributes": filepath.Join(xdg, "git/attributes"),
	}
	if !maps.Equal(got, want) || len(warnings) != 0 {
		t.Errorf("snapshots = %v, warnings %q\nwant %v", got, warnings, want)
	}
	if got, _ := collectGitConfigSnapshots(t.TempDir(), ""); len(got) != 0 {
		t.Errorf("empty home: snapshots = %v", slices.Collect(maps.Keys(got)))
	}
}
