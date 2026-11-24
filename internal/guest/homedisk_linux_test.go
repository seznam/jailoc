//go:build linux

package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

func homeTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func homeTestAccount() account {
	return account{name: "agent", uid: uint32(os.Getuid()), gid: uint32(os.Getgid())} //nolint:gosec // G115: IDs of the test process
}

func homeWrite(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func homeRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func homeJSON(t *testing.T, path string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(homeRead(t, path)), &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func homeMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("%s: err = %v, want not exist", path, err)
	}
}

func TestExtDiskMkfsArgs(t *testing.T) {
	t.Parallel()
	want := []string{"-q", "-F", "-m", "0", "-L", "jailoc-state", "-E", "nodiscard", "/dev/vdb"}
	if got := extDiskMkfsArgs("/dev/vdb", StateDiskLabel); !slices.Equal(got, want) {
		t.Fatalf("extDiskMkfsArgs = %q, want %q", got, want)
	}
}

func TestInspectExt(t *testing.T) {
	t.Parallel()
	image := func(label string) []byte {
		b := make([]byte, 4096)
		b[1080], b[1081] = 0x53, 0xEF
		copy(b[1144:], label)
		return b
	}
	tests := []struct {
		name          string
		data          []byte
		wantFormatted bool
		wantLabel     string
	}{
		{name: "empty disk", data: nil},
		{name: "zeroed disk", data: make([]byte, 4096)},
		{name: "unlabelled ext", data: image(""), wantFormatted: true},
		{name: "labelled ext", data: image(StateDiskLabel), wantFormatted: true, wantLabel: StateDiskLabel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			formatted, label, err := inspectExt(bytes.NewReader(tt.data))
			if err != nil {
				t.Fatal(err)
			}
			if formatted != tt.wantFormatted || label != tt.wantLabel {
				t.Fatalf("inspectExt = %v, %q, want %v, %q", formatted, label, tt.wantFormatted, tt.wantLabel)
			}
		})
	}
}

func TestInspectExtFS(t *testing.T) {
	t.Parallel()
	dev := filepath.Join(t.TempDir(), "disk.img")
	b := make([]byte, 4096)
	b[1080], b[1081] = 0x53, 0xEF
	copy(b[1144:], CacheDiskLabel)
	homeWrite(t, dev, string(b), 0o600)
	formatted, label, err := inspectExtFS(dev)
	if err != nil {
		t.Fatal(err)
	}
	if !formatted || label != CacheDiskLabel {
		t.Fatalf("inspectExtFS = %v, %q, want true, %q", formatted, label, CacheDiskLabel)
	}
	if _, _, err := inspectExtFS(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing device: want an error")
	}
}

func TestMountExtDiskRefusesForeignLabel(t *testing.T) {
	t.Parallel()
	dev := filepath.Join(t.TempDir(), "disk.img")
	b := make([]byte, 4096)
	b[1080], b[1081] = 0x53, 0xEF
	copy(b[1144:], "something-else")
	homeWrite(t, dev, string(b), 0o600)
	target := filepath.Join(t.TempDir(), "mnt")
	err := mountExtDisk(homeTestLogger(), dev, target, StateDiskLabel, "state disk")
	if err == nil {
		t.Fatal("want an error for a disk with another label")
	}
	for _, want := range []string{"state disk", dev, `"something-else"`, `"jailoc-state"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
	homeMissing(t, target)
}

func TestWriteMigratedMarker(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "control")
	if err := writeMigratedMarker(dir); err != nil {
		t.Fatal(err)
	}
	if got := homeRead(t, filepath.Join(dir, homeMigratedMarker)); got != "ok\n" {
		t.Fatalf("marker = %q", got)
	}
}

func TestTrimLoopStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		trimLoop(ctx, homeTestLogger(), []string{filepath.Join(t.TempDir(), "missing")}, time.Hour)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("trimLoop did not stop")
	}
}

func TestPrepareStateDir(t *testing.T) {
	t.Parallel()

	t.Run("fresh claude state gets defaults", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, "claude"))
		if err != nil || !info.IsDir() {
			t.Fatalf("claude dir: %v, %v", info, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("claude mode = %o, want 700", perm)
		}
		got := homeJSON(t, filepath.Join(root, "claude", ".claude.json"))
		if string(got["bypassPermissionsModeAccepted"]) != "true" {
			t.Fatalf(".claude.json = %v", got)
		}
	})

	t.Run("existing state default is kept", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		homeWrite(t, filepath.Join(root, "claude", ".claude.json"), `{"bypassPermissionsModeAccepted": false, "x": 1}`, 0o600)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		got := homeJSON(t, filepath.Join(root, "claude", ".claude.json"))
		if string(got["bypassPermissionsModeAccepted"]) != "false" || string(got["x"]) != "1" {
			t.Fatalf(".claude.json = %v", got)
		}
	})

	t.Run("copilot status line", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		st := &StateDisk{Agent: "copilot", Dirs: []string{"copilot"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		got := homeJSON(t, filepath.Join(root, "copilot", "settings.json"))
		var line map[string]string
		if err := json.Unmarshal(got["statusLine"], &line); err != nil {
			t.Fatalf("statusLine: %v", err)
		}
		if line["type"] != "command" || !strings.Contains(line["command"], "JAILOC_WORKSPACE") {
			t.Fatalf("statusLine = %v", line)
		}
		if string(got["storeTokenPlaintext"]) != "true" {
			t.Fatalf("storeTokenPlaintext = %s", got["storeTokenPlaintext"])
		}
	})

	t.Run("stale config mount points are removed", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		homeWrite(t, filepath.Join(root, "claude", "CLAUDE.md"), "", 0o600)
		homeWrite(t, filepath.Join(root, "claude", "settings.json"), "{}", 0o600)
		if err := os.MkdirAll(filepath.Join(root, "claude", "agents"), 0o700); err != nil {
			t.Fatal(err)
		}
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		homeMissing(t, filepath.Join(root, "claude", "CLAUDE.md"))
		homeMissing(t, filepath.Join(root, "claude", "agents"))
		if got := homeRead(t, filepath.Join(root, "claude", "settings.json")); got != "{}" {
			t.Fatalf("non-empty settings.json = %q, want it kept", got)
		}
	})

	t.Run("unknown agent only creates the directories", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		st := &StateDisk{Agent: "nope", Dirs: []string{"a", "b"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{"a", "b"} {
			if info, err := os.Stat(filepath.Join(root, d)); err != nil || !info.IsDir() {
				t.Fatalf("%s: %v, %v", d, info, err)
			}
		}
	})

	t.Run("invalid directory names are rejected", func(t *testing.T) {
		t.Parallel()
		for _, name := range []string{"", ".", "..", "a/b", "../x"} {
			root := t.TempDir()
			st := &StateDisk{Dirs: []string{name}}
			if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err == nil {
				t.Fatalf("name %q: want an error", name)
			}
		}
	})

	t.Run("a file in place of a directory is an error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		homeWrite(t, filepath.Join(root, "claude"), "x", 0o600)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, "", st, homeTestAccount()); err == nil {
			t.Fatal("want an error")
		}
	})

	t.Run("ephemeral state is emptied and persistent directories stay", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		homeWrite(t, filepath.Join(root, "cache", "keep"), "persistent", 0o600)
		homeWrite(t, filepath.Join(root, "other", "keep"), "persistent", 0o600)
		homeWrite(t, filepath.Join(root, "ephemeral", "claude", "old"), "stale", 0o600)
		homeWrite(t, filepath.Join(root, "ephemeral", "gone", "old"), "stale", 0o600)
		// A legacy directory is ignored: ephemeral state never migrates.
		legacy := t.TempDir()
		homeWrite(t, filepath.Join(legacy, "claude", "legacy"), "legacy", 0o600)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}, Ephemeral: true}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, homeTestAccount()); err != nil {
			t.Fatal(err)
		}
		homeMissing(t, filepath.Join(root, "ephemeral", "claude", "old"))
		homeMissing(t, filepath.Join(root, "ephemeral", "gone"))
		homeMissing(t, filepath.Join(root, "ephemeral", "claude", "legacy"))
		homeMissing(t, filepath.Join(root, "claude"))
		for _, p := range []string{"cache/keep", "other/keep"} {
			if got := homeRead(t, filepath.Join(root, p)); got != "persistent" {
				t.Fatalf("%s = %q", p, got)
			}
		}
		got := homeJSON(t, filepath.Join(root, "ephemeral", "claude", ".claude.json"))
		if string(got["bypassPermissionsModeAccepted"]) != "true" {
			t.Fatalf(".claude.json = %v", got)
		}
	})
}

func TestPrepareStateDirLegacyMigration(t *testing.T) {
	t.Parallel()
	acct := homeTestAccount()

	newLegacy := func(t *testing.T) string {
		t.Helper()
		legacy := t.TempDir()
		homeWrite(t, filepath.Join(legacy, "claude", "top.txt"), "top", 0o640)
		homeWrite(t, filepath.Join(legacy, "claude", "a", "b", "deep.txt"), "deep", 0o600)
		homeWrite(t, filepath.Join(legacy, "claude", "bin", "tool"), "#!/bin/sh\n", 0o755)
		if err := os.Symlink("top.txt", filepath.Join(legacy, "claude", "rel-link")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/nonexistent/target", filepath.Join(legacy, "claude", "a", "dangling")); err != nil {
			t.Fatal(err)
		}
		return legacy
	}
	snapshot := func(t *testing.T, dir string) map[string]string {
		t.Helper()
		out := map[string]string{}
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			info, err := d.Info()
			if err != nil {
				return err
			}
			switch {
			case info.Mode()&os.ModeSymlink != 0:
				l, _ := os.Readlink(p)
				out[rel] = "link:" + l
			case info.IsDir():
				out[rel] = "dir"
			default:
				b, _ := os.ReadFile(p) //nolint:gosec // G304: test file
				out[rel] = info.Mode().Perm().String() + ":" + string(b)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("copies the legacy directory and leaves it untouched", func(t *testing.T) {
		t.Parallel()
		legacy, root := newLegacy(t), t.TempDir()
		before := snapshot(t, legacy)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		if got := snapshot(t, legacy); !mapsEqual(got, before) {
			t.Fatalf("legacy source changed:\n got %v\nwant %v", got, before)
		}
		got := snapshot(t, filepath.Join(root, "claude"))
		for p, want := range map[string]string{
			"top.txt":      "-rw-r-----:top",
			"a/b/deep.txt": "-rw-------:deep",
			"bin/tool":     "-rwxr-xr-x:#!/bin/sh\n",
			"rel-link":     "link:top.txt",
			"a/dangling":   "link:/nonexistent/target",
			"a/b":          "dir",
		} {
			if got[p] != want {
				t.Errorf("%s = %q, want %q", p, got[p], want)
			}
		}
		// Defaults are applied on top of the migrated files.
		if _, ok := got[".claude.json"]; !ok {
			t.Error("defaults not applied to the migrated directory")
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), migratingPrefix) {
				t.Errorf("temporary %s left behind", e.Name())
			}
		}
	})

	t.Run("an existing directory is not overwritten", func(t *testing.T) {
		t.Parallel()
		legacy, root := newLegacy(t), t.TempDir()
		homeWrite(t, filepath.Join(root, "claude", "mine"), "disk", 0o600)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		if got := homeRead(t, filepath.Join(root, "claude", "mine")); got != "disk" {
			t.Fatalf("mine = %q", got)
		}
		homeMissing(t, filepath.Join(root, "claude", "top.txt"))
	})

	t.Run("a second boot does not copy again", func(t *testing.T) {
		t.Parallel()
		legacy, root := newLegacy(t), t.TempDir()
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(root, "claude", "top.txt")); err != nil {
			t.Fatal(err)
		}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		homeMissing(t, filepath.Join(root, "claude", "top.txt"))
	})

	t.Run("an interrupted copy is started again", func(t *testing.T) {
		t.Parallel()
		legacy, root := newLegacy(t), t.TempDir()
		homeWrite(t, filepath.Join(root, migratingPrefix+"claude", "half", "junk"), "partial", 0o600)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		homeMissing(t, filepath.Join(root, migratingPrefix+"claude"))
		homeMissing(t, filepath.Join(root, "claude", "half"))
		if got := homeRead(t, filepath.Join(root, "claude", "a", "b", "deep.txt")); got != "deep" {
			t.Fatalf("deep.txt = %q", got)
		}
	})

	t.Run("only directories the agent has are migrated", func(t *testing.T) {
		t.Parallel()
		legacy, root := t.TempDir(), t.TempDir()
		homeWrite(t, filepath.Join(legacy, "file-not-dir"), "x", 0o600)
		st := &StateDisk{Dirs: []string{"file-not-dir", "absent"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err != nil {
			t.Fatal(err)
		}
		for _, d := range st.Dirs {
			if info, err := os.Stat(filepath.Join(root, d)); err != nil || !info.IsDir() {
				t.Fatalf("%s: %v, %v", d, info, err)
			}
		}
		entries, err := os.ReadDir(filepath.Join(root, "file-not-dir"))
		if err != nil || len(entries) != 0 {
			t.Fatalf("file-not-dir: %v, %v", entries, err)
		}
	})

	t.Run("a failed copy leaves no directory and the source alone", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root reads every file")
		}
		legacy, root := newLegacy(t), t.TempDir()
		secret := filepath.Join(legacy, "claude", "unreadable")
		homeWrite(t, secret, "x", 0o000)
		st := &StateDisk{Agent: "claude", Dirs: []string{"claude"}}
		if err := prepareStateDir(homeTestLogger(), root, legacy, st, acct); err == nil {
			t.Fatal("want an error")
		}
		homeMissing(t, filepath.Join(root, "claude"))
		homeMissing(t, filepath.Join(root, migratingPrefix+"claude"))
		if _, err := os.Lstat(secret); err != nil {
			t.Fatalf("source file: %v", err)
		}
	})
}

func TestPrepareStateDirOwnership(t *testing.T) {
	t.Parallel()
	if os.Geteuid() != 0 {
		t.Skip("needs root to give files to another owner")
	}
	root := t.TempDir()
	homeWrite(t, filepath.Join(root, "claude", "sub", "f"), "x", 0o600)
	for _, p := range []string{"claude", "claude/sub", "claude/sub/f"} {
		if err := os.Lchown(filepath.Join(root, p), 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	acct := account{name: "agent", uid: 4242, gid: 4343}
	st := &StateDisk{Dirs: []string{"claude"}}
	if err := prepareStateDir(homeTestLogger(), root, "", st, acct); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"claude", "claude/sub", "claude/sub/f"} {
		uid, gid := homeOwner(t, filepath.Join(root, p))
		if uid != 4242 || gid != 4343 {
			t.Errorf("%s owned by %d:%d, want 4242:4343", p, uid, gid)
		}
	}
}

func TestPrepareCacheDir(t *testing.T) {
	t.Parallel()
	acct := homeTestAccount()

	t.Run("creates the cache directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		if err := prepareCacheDir(homeTestLogger(), root, filepath.Join(root, "no-legacy"), acct); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(root, "cache"))
		if err != nil || !info.IsDir() {
			t.Fatalf("cache: %v, %v", info, err)
		}
		if perm := info.Mode().Perm(); perm != 0o700 {
			t.Fatalf("cache mode = %o, want 700", perm)
		}
		uid, gid := homeOwner(t, filepath.Join(root, "cache"))
		if uid != acct.uid || gid != acct.gid {
			t.Fatalf("cache owned by %d:%d, want %d:%d", uid, gid, acct.uid, acct.gid)
		}
	})

	t.Run("keeps existing content", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		homeWrite(t, filepath.Join(root, "cache", "pip", "wheel"), "w", 0o600)
		if err := prepareCacheDir(homeTestLogger(), root, "", acct); err != nil {
			t.Fatal(err)
		}
		if got := homeRead(t, filepath.Join(root, "cache", "pip", "wheel")); got != "w" {
			t.Fatalf("wheel = %q", got)
		}
	})

	t.Run("migrates the legacy cache once", func(t *testing.T) {
		t.Parallel()
		root, legacy := t.TempDir(), t.TempDir()
		homeWrite(t, filepath.Join(legacy, "cache", "go", "mod"), "m", 0o600)
		if err := prepareCacheDir(homeTestLogger(), root, legacy, acct); err != nil {
			t.Fatal(err)
		}
		if got := homeRead(t, filepath.Join(root, "cache", "go", "mod")); got != "m" {
			t.Fatalf("mod = %q", got)
		}
		if got := homeRead(t, filepath.Join(legacy, "cache", "go", "mod")); got != "m" {
			t.Fatalf("legacy mod = %q, want it untouched", got)
		}
	})
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func homeOwner(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("%s: no stat", path)
	}
	return st.Uid, st.Gid
}
