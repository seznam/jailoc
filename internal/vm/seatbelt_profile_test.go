package vm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/go-microvm/runner"
)

// realDir is t.TempDir() with symlinks resolved (macOS keeps temp dirs behind
// /var -> /private/var), the form the profile must contain.
func realDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func profileLines(t *testing.T, cfg runner.Config, runnerPath string) []string {
	t.Helper()
	profile, err := seatbeltProfile(cfg, runnerPath)
	if err != nil {
		t.Fatalf("seatbeltProfile: %v", err)
	}
	return strings.Split(strings.TrimSuffix(profile, "\n"), "\n")
}

func assertRule(t *testing.T, lines []string, want string) {
	t.Helper()
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Errorf("profile lacks rule %s\nprofile:\n%s", want, strings.Join(lines, "\n"))
}

func assertNoRuleContaining(t *testing.T, lines []string, substr string) {
	t.Helper()
	for _, l := range lines {
		if strings.Contains(l, substr) {
			t.Errorf("profile has unexpected rule %s", l)
		}
	}
}

func TestSeatbeltProfileHeader(t *testing.T) {
	t.Parallel()
	runnerPath := filepath.Join(realDir(t), RunnerBinary)
	lines := profileLines(t, runner.Config{}, runnerPath)
	want := []string{
		"(version 1)",
		"(deny default)",
		`(import "system.sb")`,
		`(allow process-exec (literal "` + runnerPath + `"))`,
		`(allow file-read* file-map-executable (literal "` + runnerPath + `"))`,
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("profile =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

func TestSeatbeltProfileRules(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	runnerPath := filepath.Join(base, "runtime", RunnerBinary)
	lib1 := filepath.Join(base, "lib1")
	lib2 := filepath.Join(base, "lib2")
	cfg := runner.Config{
		RootPath:   filepath.Join(base, "state", "microvm", "rootfs"),
		LibDir:     lib1 + string(filepath.ListSeparator) + string(filepath.ListSeparator) + lib2,
		NetSocket:  filepath.Join(base, "state", "nic.sock"),
		ConsoleLog: filepath.Join(base, "state", "microvm", "console.log"),
		VMLogPath:  filepath.Join(base, "state", "microvm", "vm.log"),
		VirtioFS: []runner.VirtioFSMount{
			{Tag: "rw", HostPath: filepath.Join(base, "rw")},
			{Tag: "ro", HostPath: filepath.Join(base, "ro"), ReadOnly: true},
		},
		Disks: []runner.Disk{
			{ID: "docker", HostPath: filepath.Join(base, "state", "docker.img")},
			{ID: "base", HostPath: filepath.Join(base, "state", "base.img"), ReadOnly: true},
		},
		VsockPorts: []runner.VsockPort{{SocketPath: filepath.Join(base, "state", "vsock.sock")}},
	}
	lines := profileLines(t, cfg, runnerPath)

	for _, want := range []string{
		`(allow process-exec (literal "` + runnerPath + `"))`,
		`(allow file-read* file-map-executable (literal "` + runnerPath + `"))`,
		`(allow file-read* file-map-executable (subpath "` + lib1 + `"))`,
		`(allow file-read* file-map-executable (subpath "` + lib2 + `"))`,
		`(allow file-read* file-write* (subpath "` + cfg.RootPath + `"))`,
		`(allow file-read* file-write* (subpath "` + filepath.Join(base, "rw") + `"))`,
		`(allow file-read* (subpath "` + filepath.Join(base, "ro") + `"))`,
		`(allow file-read* file-write-data (literal "` + filepath.Join(base, "state", "docker.img") + `"))`,
		`(allow file-read* (literal "` + filepath.Join(base, "state", "base.img") + `"))`,
		`(allow file-read* file-write* (literal "` + cfg.ConsoleLog + `"))`,
		`(allow file-read* file-write* (literal "` + cfg.VMLogPath + `"))`,
		`(allow network-outbound (remote unix-socket (path-literal "` + cfg.NetSocket + `")))`,
		`(allow network-bind network-inbound (local unix-socket (path-literal "` + cfg.VsockPorts[0].SocketPath + `")))`,
		`(allow file-read* file-write* (literal "` + cfg.VsockPorts[0].SocketPath + `"))`,
	} {
		assertRule(t, lines, want)
	}

	// Read-only shares and disks never get write access.
	for _, l := range lines {
		if strings.Contains(l, filepath.Join(base, "ro")) && strings.Contains(l, "write") {
			t.Errorf("read-only share is writable: %s", l)
		}
		if strings.Contains(l, "base.img") && strings.Contains(l, "write") {
			t.Errorf("read-only disk is writable: %s", l)
		}
	}
	// The log directory and the state directory are never granted as a whole.
	for _, dir := range []string{filepath.Join(base, "state"), filepath.Join(base, "state", "microvm")} {
		assertNoRuleContaining(t, lines, `(subpath "`+dir+`")`)
		assertNoRuleContaining(t, lines, `(literal "`+dir+`")`)
	}
}

// TestSeatbeltProfileGrantsTmpDiskOnly mirrors TestVMMRulesGrantTmpDiskOnly:
// the tmp disk is reachable through its Disks entry alone, with a literal
// rule, so the state directory, the lock file and siblings stay out of reach.
func TestSeatbeltProfileGrantsTmpDiskOnly(t *testing.T) {
	t.Parallel()
	state := filepath.Join(realDir(t), "state")
	tmp := tmpDiskPath(state)
	cfg := runner.Config{Disks: []runner.Disk{
		{ID: "docker", HostPath: diskPath(state)},
		{ID: "tmp", HostPath: tmp},
	}}
	lines := profileLines(t, cfg, filepath.Join(state, RunnerBinary))

	assertRule(t, lines, `(allow file-read* file-write-data (literal "`+tmp+`"))`)
	assertNoRuleContaining(t, lines, "(subpath")
	assertNoRuleContaining(t, lines, tmpLockPath(state))
	assertNoRuleContaining(t, lines, `"`+state+`"`)
	for _, l := range lines {
		if strings.Contains(l, tmp) && !strings.Contains(l, `(literal "`+tmp+`")`) {
			t.Errorf("tmp disk rule is not a literal: %s", l)
		}
	}
}

func TestSeatbeltProfileCanonicalizesPaths(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	target := filepath.Join(base, "target")
	if err := os.MkdirAll(filepath.Join(target, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	fileLink := filepath.Join(base, "file-link")
	if err := os.WriteFile(filepath.Join(target, "disk.img"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(target, "disk.img"), fileLink); err != nil {
		t.Fatal(err)
	}

	cfg := runner.Config{
		VirtioFS: []runner.VirtioFSMount{{Tag: "share", HostPath: link}},
		Disks:    []runner.Disk{{ID: "d", HostPath: fileLink}},
		// A log that does not exist yet behind a symlinked directory, and a
		// path with ".." components.
		VMLogPath:  filepath.Join(link, "sub", "new", "vm.log"),
		ConsoleLog: filepath.Join(link, "sub", "..", "console.log"),
	}
	lines := profileLines(t, cfg, filepath.Join(link, RunnerBinary))

	for _, want := range []string{
		`(allow process-exec (literal "` + filepath.Join(target, RunnerBinary) + `"))`,
		`(allow file-read* file-write* (subpath "` + target + `"))`,
		`(allow file-read* file-write-data (literal "` + filepath.Join(target, "disk.img") + `"))`,
		`(allow file-read* file-write* (literal "` + filepath.Join(target, "sub", "new", "vm.log") + `"))`,
		`(allow file-read* file-write* (literal "` + filepath.Join(target, "console.log") + `"))`,
	} {
		assertRule(t, lines, want)
	}
	assertNoRuleContaining(t, lines, `"`+link)
	assertNoRuleContaining(t, lines, `"`+fileLink+`"`)
}

func TestSeatbeltProfileFollowsLibDirDylibSymlinks(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	libDir := filepath.Join(base, "lib")
	cellar := filepath.Join(base, "cellar", "libkrun", "lib")
	for _, d := range []string{libDir, cellar} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	realLib := filepath.Join(cellar, "libkrun.1.dylib")
	sibling := filepath.Join(cellar, "sibling.txt")
	for _, f := range []string{realLib, sibling} {
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Two symlinks to the same external dylib (should deduplicate), one
	// non-dylib symlink (should be ignored), and one broken dylib symlink
	// (should be ignored rather than failing profile generation).
	for name, target := range map[string]string{
		"libkrun.dylib":   realLib,
		"libkrun.1.dylib": realLib,
		"notes.txt":       sibling,
		"broken.dylib":    filepath.Join(cellar, "missing.dylib"),
	} {
		if err := os.Symlink(target, filepath.Join(libDir, name)); err != nil {
			t.Fatal(err)
		}
	}

	cfg := runner.Config{LibDir: libDir}
	lines := profileLines(t, cfg, filepath.Join(base, RunnerBinary))

	wantTargetRule := `(allow file-read* file-map-executable (literal "` + realLib + `"))`
	assertRule(t, lines, `(allow file-read* file-map-executable (subpath "`+libDir+`"))`)
	assertRule(t, lines, wantTargetRule)
	count := 0
	for _, l := range lines {
		if l == wantTargetRule {
			count++
		}
	}
	if count != 1 {
		t.Errorf("resolved dylib rule appeared %d times, want 1", count)
	}
	assertNoRuleContaining(t, lines, sibling)
	assertNoRuleContaining(t, lines, `(subpath "`+cellar+`")`)
}

func TestCanonicalPath(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	if err := os.MkdirAll(filepath.Join(base, "real"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real"), filepath.Join(base, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "real", "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "real", "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(base, "real", "nested"), filepath.Join(base, "nested-link")); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{"existing symlink", filepath.Join(base, "alias"), filepath.Join(base, "real")},
		{"missing leaf behind symlink", filepath.Join(base, "alias", "a", "b.sock"), filepath.Join(base, "real", "a", "b.sock")},
		{"dot dot after symlink", filepath.Join(base, "alias", "x", "..", "y"), filepath.Join(base, "real", "y")},
		// Literal "symlink/../..." strings (not cleaned by filepath.Join) must
		// follow the symlink before applying "..".
		{"literal dot dot through symlink to existing", filepath.Join(base, "nested-link") + "/../lib", filepath.Join(base, "real", "lib")},
		{"literal dot dot through symlink to missing leaf", filepath.Join(base, "nested-link") + "/../missing/vm.log", filepath.Join(base, "real", "missing", "vm.log")},
		{"relative path", "missing-leaf", filepath.Join(realCwd, "missing-leaf")},
		{"plain missing path", filepath.Join(base, "none"), filepath.Join(base, "none")},
		{"root", "/", "/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalPath(tt.path)
			if err != nil {
				t.Fatalf("canonicalPath(%q): %v", tt.path, err)
			}
			if got != tt.want {
				t.Errorf("canonicalPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}

	if _, err := canonicalPath(""); err == nil {
		t.Error("canonicalPath(\"\") succeeded, want an error")
	}
}

func TestSBPLQuote(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"plain", "/a/b", `"/a/b"`, false},
		{"space", "/a b", `"/a b"`, false},
		{"double quote", `/a"b`, `"/a\"b"`, false},
		{"backslash", `/a\b`, `"/a\\b"`, false},
		{"quote after backslash", `/a\"b`, `"/a\\\"b"`, false},
		{"closing paren", `/a")(allow default)("`, `"/a\")(allow default)(\""`, false},
		{"NUL", "/a\x00b", "", true},
		{"newline", "/a\nb", "", true},
		{"carriage return", "/a\rb", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := sbplQuote(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("sbplQuote(%q) error = %v, wantErr %v", tt.in, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("sbplQuote(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

func TestSeatbeltProfileQuotesPaths(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	nasty := filepath.Join(base, `we"ird\dir`)
	cfg := runner.Config{VirtioFS: []runner.VirtioFSMount{{Tag: "x", HostPath: nasty}}}
	lines := profileLines(t, cfg, filepath.Join(base, RunnerBinary))
	assertRule(t, lines, `(allow file-read* file-write* (subpath "`+filepath.Join(base, `we\"ird\\dir`)+`"))`)
}

func TestSeatbeltProfileErrors(t *testing.T) {
	t.Parallel()
	base := realDir(t)
	runnerPath := filepath.Join(base, RunnerBinary)
	tests := []struct {
		name       string
		cfg        runner.Config
		runnerPath string
	}{
		{"empty runner path", runner.Config{}, ""},
		{"empty share path", runner.Config{VirtioFS: []runner.VirtioFSMount{{Tag: "x"}}}, runnerPath},
		{"empty disk path", runner.Config{Disks: []runner.Disk{{ID: "x"}}}, runnerPath},
		{"empty vsock path", runner.Config{VsockPorts: []runner.VsockPort{{}}}, runnerPath},
		{"NUL in share path", runner.Config{VirtioFS: []runner.VirtioFSMount{{Tag: "x", HostPath: "/a\x00b"}}}, runnerPath},
		{"NUL in net socket", runner.Config{NetSocket: "/a\x00b"}, runnerPath},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if profile, err := seatbeltProfile(tt.cfg, tt.runnerPath); err == nil {
				t.Fatalf("seatbeltProfile succeeded:\n%s", profile)
			}
		})
	}
}
