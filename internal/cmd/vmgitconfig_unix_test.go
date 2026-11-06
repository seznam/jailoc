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
		".ssh/allowed_signers":    "me@example.com " + sshPublicKey(t),
		".ssh/id_ed25519.pub":     sshPublicKey(t),
		".ssh/template":           "subject\n",
	})
	got, warnings := collectGitConfigSnapshots(home, "")
	want := map[string]string{
		guestHome + "/.gitconfig":            filepath.Join(home, ".gitconfig") + " (generated)",
		guestHome + "/.config/git/extra.inc": filepath.Join(home, ".config/git/extra.inc") + " (generated)",
		guestHome + "/work.inc":              filepath.Join(home, "work.inc") + " (generated)",
		guestHome + "/.gitignore_global":     filepath.Join(home, ".gitignore_global") + " (generated)",
		guestHome + "/.ssh/allowed_signers":  filepath.Join(home, ".ssh/allowed_signers") + " (generated)",
		guestHome + "/.ssh/id_ed25519.pub":   filepath.Join(home, ".ssh/id_ed25519.pub") + " (generated)",
		guestHome + "/.config/git/config":    filepath.Join(home, ".config/git/config") + " (generated)",
		guestHome + "/.config/git/ignore":    filepath.Join(home, ".config/git/ignore") + " (generated)",
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
		guestHome + "/.config/git/attributes": filepath.Join(xdg, "git/attributes") + " (generated)",
	}
	if !maps.Equal(got, want) || len(warnings) != 0 {
		t.Errorf("snapshots = %v, warnings %q\nwant %v", got, warnings, want)
	}
	if got, _ := collectGitConfigSnapshots(t.TempDir(), ""); len(got) != 0 {
		t.Errorf("empty home: snapshots = %v", slices.Collect(maps.Keys(got)))
	}
}

// gitConfigData runs gitConfigSnapshots and returns the staged content by
// destination and the warnings.
func gitConfigData(home, xdg string) (map[string]string, string) {
	var warnings []string
	out := map[string]string{}
	for _, s := range gitConfigSnapshots(home, xdg, func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}) {
		out[s.Container] = string(s.data)
	}
	return out, strings.Join(warnings, "\n")
}

func TestGitConfigSnapshotsCredentials(t *testing.T) {
	t.Parallel()
	const private = "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----\n"
	tests := []struct {
		name    string
		config  string
		links   map[string]string // link → target, relative to home
		hard    map[string]string // hard link → target, relative to home
		xdg     string            // relative to home
		warning string
	}{
		{name: "credential store", config: "[core]\n\texcludesFile = ~/.git-credentials\n", warning: "/.git-credentials is a Git credential store"},
		{name: "XDG credential store", config: "[core]\n\texcludesFile = ~/.config/git/credentials\n", warning: "/.config/git/credentials is a Git credential store"},
		{name: "relocated XDG credential store", config: "[core]\n\texcludesFile = ~/.xdg/git/credentials\n", xdg: ".xdg", warning: "/.xdg/git/credentials is a Git credential store"},
		{name: "default credential store with relocated XDG", config: "[core]\n\texcludesFile = ~/.config/git/credentials\n", xdg: ".xdg", warning: "is a Git credential store"},
		{name: "public file kind in credential store", config: "[user]\n\tsigningKey = ~/.git-credentials\n", warning: "is a Git credential store"},
		{name: "symbolic link to a credentials file", config: "[core]\n\texcludesFile = ~/git-ignore\n", links: map[string]string{"git-ignore": ".aws/credentials"}, warning: "/git-ignore is in ~/.aws"},
		{name: "symbolic link to a credentials directory", config: "[commit]\n\ttemplate = ~/keys/id_ed25519\n", links: map[string]string{"keys": ".ssh"}, warning: "/keys/id_ed25519 is in ~/.ssh"},
		{name: "relative include", config: "[include]\n\tpath = .ssh/id_ed25519\n", warning: "/.ssh/id_ed25519 is in ~/.ssh"},
		{name: "symbolic link in a credentials directory", config: "[core]\n\texcludesFile = ~/.ssh/deploy\n", links: map[string]string{".ssh/deploy": "private-key"}, warning: "/.ssh/deploy is in ~/.ssh"},
		{name: "hard link to a credentials file", config: "[core]\n\texcludesFile = ~/git-ignore\n", hard: map[string]string{"git-ignore": ".ssh/id_ed25519"}, warning: "/git-ignore is in ~/.ssh"},
		{name: "symbolic link to a linked credentials subdirectory", config: "[core]\n\texcludesFile = ~/ignore-link\n", links: map[string]string{".ssh/work": "private-keys", "ignore-link": ".ssh/work/id_ed25519"}, warning: "/ignore-link is in ~/.ssh"},
		{name: "hard link into a linked credentials subdirectory", config: "[core]\n\texcludesFile = ~/git-ignore\n", links: map[string]string{".ssh/work": "private-keys"}, hard: map[string]string{"git-ignore": "private-keys/id_ed25519"}, warning: "/git-ignore is in ~/.ssh"},
		{name: "private key as allowed signers", config: "[gpg \"ssh\"]\n\tallowedSignersFile = ~/.ssh/id_ed25519\n", warning: "is not an allowed signers file"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			gitConfigFiles(t, home, map[string]string{ //nolint:gosec // G101: test fixture
				".gitconfig":              tt.config,
				".git-credentials":        "https://user:secret@example.com\n",
				".config/git/credentials": "https://user:secret@example.com\n",
				".xdg/git/credentials":    "https://user:secret@example.com\n",
				".aws/credentials":        "[default]\nsecret\n",
				".ssh/id_ed25519":         private,
				"private-key":             private,
				"private-keys/id_ed25519": private,
			})
			for link, target := range tt.links {
				if err := os.Symlink(filepath.Join(home, target), filepath.Join(home, link)); err != nil {
					t.Fatal(err)
				}
			}
			for link, target := range tt.hard {
				if err := os.Link(filepath.Join(home, target), filepath.Join(home, link)); err != nil {
					t.Fatal(err)
				}
			}
			xdg := ""
			if tt.xdg != "" {
				xdg = filepath.Join(home, tt.xdg)
			}
			got, warnings := gitConfigData(home, xdg)
			for dest, data := range got {
				if strings.Contains(data, "secret") {
					t.Errorf("%s carries a credential: %q", dest, data)
				}
			}
			if len(got) != 1 {
				t.Errorf("snapshots = %v, want only ~/.gitconfig", slices.Collect(maps.Keys(got)))
			}
			assertContains(t, warnings, tt.warning)
			assertContains(t, warnings, "it is not copied to the VM")
		})
	}
}

func TestGitConfigSnapshotsPublicFiles(t *testing.T) {
	t.Parallel()
	key := sshPublicKey(t)
	other := sshPublicKey(t)
	keyNoComment := strings.TrimSuffix(key, "\n")
	tests := []struct {
		name, file, want string
	}{
		{
			name: "signing key next to a private key",
			file: "[user]\n\tsigningKey = ~/.ssh/key\n",
			want: key,
		},
		{
			name: "allowed signers with options and comments",
			file: "[gpg \"ssh\"]\n\tallowedSignersFile = ~/.ssh/allowed_signers\n",
			want: "me@example.com namespaces=\"git\",valid-after=\"20240101\" " + key + `"a b@example.com" cert-authority ` + other,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			gitConfigFiles(t, home, map[string]string{
				".gitconfig": tt.file,
				".ssh/key":   "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret\n-----END OPENSSH PRIVATE KEY-----\n" + keyNoComment + " me@laptop\n",
				".ssh/allowed_signers": "# team\n\n" +
					"me@example.com namespaces=\"git\",valid-after=\"20240101\" " + keyNoComment + " laptop\n" +
					`"a b@example.com" cert-authority ` + other,
			})
			got, warnings := gitConfigData(home, "")
			delete(got, guestGitConfig)
			if len(got) != 1 || warnings != "" {
				t.Fatalf("snapshots = %v, warnings %q, want one", got, warnings)
			}
			for _, data := range got {
				if data != tt.want {
					t.Errorf("data =\n%s\nwant\n%s", data, tt.want)
				}
			}
		})
	}
}

func TestAllowedSignersRejects(t *testing.T) {
	t.Parallel()
	key := sshPublicKey(t)
	for _, in := range []string{
		"",
		"# only a comment\n",
		"me@example.com command=\"sh\" " + key,
		`"me@example.com ` + key,
		"me@example.com\n",
		"me@example.com " + key + "garbage line\n",
	} {
		if out, ok := allowedSigners([]byte(in)); ok {
			t.Errorf("allowedSigners(%q) = %q, want rejected", in, out)
		}
	}
}

func TestGitConfigSnapshotsRelocatedXDG(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	gitConfigFiles(t, home, map[string]string{
		".gitconfig":      "[includeIf \"gitdir:~/src/\"]\n\tpath = ~/.xdg/git/config\n",
		".xdg/git/config": "[user]\n\tname = me\n",
	})
	got, warnings := collectGitConfigSnapshots(home, filepath.Join(home, ".xdg"))
	src := filepath.Join(home, ".xdg/git/config") + " (generated)"
	if got[guestHome+"/.config/git/config"] != src || got[guestHome+"/.xdg/git/config"] != src || len(warnings) != 0 {
		t.Errorf("snapshots = %v, warnings %q, want %s at both guest paths", got, warnings, src)
	}
}

func TestGitConfigSnapshotsRelativeIncludeRoots(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	home := filepath.Join(base, "home")
	gitConfigFiles(t, base, map[string]string{
		"private.inc":        "[user]\n\tname = secret\n",
		"home/git/config":    "[include]\n\tpath = ../../private.inc\n",
		"xdg/git/config":     "[include]\n\tpath = work.inc\n",
		"xdg/git/work.inc":   "[user]\n\temail = me@example.com\n",
		"home/.gitconfig":    "",
		"home/.config/.keep": "",
	})

	// With XDG_CONFIG_HOME=$HOME, a relative include must not leave the home
	// directory even though its guest path stays inside it.
	got, warnings := gitConfigData(home, home)
	if _, ok := got[guestHome+"/private.inc"]; ok {
		t.Errorf("snapshots = %v, want no file from outside the home directory", got)
	}
	assertContains(t, warnings, "include.path "+filepath.Join(base, "private.inc")+" is outside the home directory; it is not copied to the VM")

	// A relocated XDG directory outside the home directory keeps its
	// relative includes.
	got, warnings = gitConfigData(home, filepath.Join(base, "xdg"))
	if got[guestHome+"/.config/git/work.inc"] != "[user]\n\temail = me@example.com\n" || warnings != "" {
		t.Errorf("snapshots = %v, warnings %q, want the XDG include", got, warnings)
	}
}

func TestGitConfigSnapshotsGuestPathConflict(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	gitConfigFiles(t, home, map[string]string{
		".gitconfig":         "[include]\n\tpath = ~/.gitconfig\n[core]\n\texcludesFile = ~/.config/git/ignore\n",
		".xdg/git/ignore":    "*.swp\n",
		".config/git/ignore": "*.o\n",
	})
	got, warnings := gitConfigData(home, filepath.Join(home, ".xdg"))
	if got[guestHome+"/.config/git/ignore"] != "*.swp\n" || len(got) != 2 {
		t.Errorf("snapshots = %v, want the XDG ignore file at its default path", got)
	}
	assertContains(t, warnings, "core.excludesFile "+filepath.Join(home, ".config/git/ignore")+" is not copied to the VM: "+
		filepath.Join(home, ".xdg/git/ignore")+" already takes its path there")
	if strings.Count(warnings, "\n") != 0 {
		t.Errorf("warnings = %q, want one", warnings)
	}
}

func TestGitConfigSnapshotsIncludeLimit(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	var b strings.Builder
	b.WriteString("[include]\n")
	files := map[string]string{}
	for i := range maxConfigFiles - 1 {
		name := fmt.Sprintf("inc%d", i)
		files[name] = "[user]\n"
		b.WriteString("\tpath = ~/" + name + "\n")
	}
	// Reading the excess include, a directory, would produce a warning.
	b.WriteString("\tpath = ~/excess\n")
	files[".gitconfig"] = b.String()
	gitConfigFiles(t, home, files)
	if err := os.Mkdir(filepath.Join(home, "excess"), 0o700); err != nil {
		t.Fatal(err)
	}
	got, warnings := gitConfigData(home, "")
	if len(got) != maxConfigFiles {
		t.Errorf("%d snapshots, want %d", len(got), maxConfigFiles)
	}
	if want := fmt.Sprintf("more than %d configuration files are included; %s and later includes are not copied to the VM", maxConfigFiles, filepath.Join(home, "excess")); warnings != want {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
}

func TestGitConfigSnapshotsIncompleteCredentialScan(t *testing.T) {
	prev := maxProtectedEntries
	maxProtectedEntries = 2
	t.Cleanup(func() { maxProtectedEntries = prev })

	home := t.TempDir()
	gitConfigFiles(t, home, map[string]string{
		".gitconfig":   "[core]\n\texcludesFile = ~/keys/private\n",
		".ssh/a":       "a\n",
		".ssh/b":       "b\n",
		"keys/private": "secret\n",
	})
	if err := os.Symlink(filepath.Join(home, "keys"), filepath.Join(home, ".ssh", "zz-linked")); err != nil {
		t.Fatal(err)
	}
	got, warnings := gitConfigData(home, "")
	if len(got) != 0 {
		t.Errorf("snapshots = %v, want none when credential scan is incomplete", got)
	}
	assertContains(t, warnings, "is not checked because ~/.ssh has more than 2 entries; it is not copied to the VM")
}
