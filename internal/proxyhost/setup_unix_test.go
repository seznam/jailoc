//go:build darwin || linux

package proxyhost

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHelperBinaryAndServiceUnitPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos       string
		wantHelper string
		wantUnit   string
	}{
		{"darwin", "/Library/PrivilegedHelperTools/jailoc-listener-broker", "/Library/LaunchDaemons/cz.seznam.jailoc.listener-broker.plist"},
		{"linux", "/usr/local/libexec/jailoc-listener-broker", "/etc/systemd/system/jailoc-listener-broker.service"},
		{"windows", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			t.Parallel()
			if got := HelperBinaryPath(tt.goos); got != tt.wantHelper {
				t.Errorf("HelperBinaryPath(%q) = %q, want %q", tt.goos, got, tt.wantHelper)
			}
			if got := ServiceUnitPath(tt.goos); got != tt.wantUnit {
				t.Errorf("ServiceUnitPath(%q) = %q, want %q", tt.goos, got, tt.wantUnit)
			}
		})
	}
}

func TestServiceConstants(t *testing.T) {
	t.Parallel()

	if LaunchDaemonLabel != "cz.seznam.jailoc.listener-broker" {
		t.Errorf("LaunchDaemonLabel = %q", LaunchDaemonLabel)
	}
	if SystemdServiceName != "jailoc-listener-broker.service" {
		t.Errorf("SystemdServiceName = %q", SystemdServiceName)
	}
	if NSSCertNickname != "jailoc proxy CA" {
		t.Errorf("NSSCertNickname = %q", NSSCertNickname)
	}
}

func TestRenderLaunchDaemonPlist(t *testing.T) {
	t.Parallel()

	const want = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>cz.seznam.jailoc.listener-broker</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Library/PrivilegedHelperTools/jailoc-listener-broker</string>
		<string>--uid</string>
		<string>501</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`
	tests := []struct {
		name       string
		helperPath string
		uid        uint32
		want       string
		wantSubs   []string
		wantErr    bool
	}{
		{name: "default helper", helperPath: HelperBinaryPath("darwin"), uid: 501, want: want},
		{
			name:       "xml special characters are escaped",
			helperPath: "/opt/a&b/<helper>",
			uid:        1000,
			wantSubs:   []string{"<string>/opt/a&amp;b/&lt;helper&gt;</string>", "<string>1000</string>"},
		},
		{name: "max uid", helperPath: "/h", uid: 4294967295, wantSubs: []string{"<string>4294967295</string>"}},
		{name: "relative path", helperPath: "helper", uid: 501, wantErr: true},
		{name: "empty path", helperPath: "", uid: 501, wantErr: true},
		{name: "root uid", helperPath: "/h", uid: 0, wantErr: true},
		{name: "newline in path", helperPath: "/h\n/x", uid: 501, wantErr: true},
		{name: "nul in path", helperPath: "/h\x00", uid: 501, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderLaunchDaemonPlist(tt.helperPath, tt.uid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RenderLaunchDaemonPlist() = %q, want error", got)
				}
				if got != nil {
					t.Errorf("RenderLaunchDaemonPlist() = %q on error, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderLaunchDaemonPlist() error = %v", err)
			}
			if tt.want != "" && string(got) != tt.want {
				t.Errorf("RenderLaunchDaemonPlist() =\n%s\nwant\n%s", got, tt.want)
			}
			for _, s := range tt.wantSubs {
				if !strings.Contains(string(got), s) {
					t.Errorf("RenderLaunchDaemonPlist() = %q, missing %q", got, s)
				}
			}
		})
	}
}

func TestRenderSystemdUnit(t *testing.T) {
	t.Parallel()

	const want = `[Unit]
Description=jailoc proxy listener broker

[Service]
Type=simple
ExecStart=/usr/local/libexec/jailoc-listener-broker --uid 1000
Restart=always
RestartSec=1

[Install]
WantedBy=multi-user.target
`
	tests := []struct {
		name       string
		helperPath string
		uid        uint32
		want       string
		wantSubs   []string
		wantErr    bool
	}{
		{name: "default helper", helperPath: HelperBinaryPath("linux"), uid: 1000, want: want},
		{name: "custom path", helperPath: "/opt/jl/broker", uid: 42, wantSubs: []string{"ExecStart=/opt/jl/broker --uid 42\n"}},
		{name: "relative path", helperPath: "broker", uid: 1000, wantErr: true},
		{name: "empty path", helperPath: "", uid: 1000, wantErr: true},
		{name: "root uid", helperPath: "/h", uid: 0, wantErr: true},
		{name: "space in path", helperPath: "/opt/a b/broker", uid: 1000, wantErr: true},
		{name: "newline in path", helperPath: "/h\nExecStart=/evil", uid: 1000, wantErr: true},
		{name: "percent specifier", helperPath: "/opt/%h/broker", uid: 1000, wantErr: true},
		{name: "dollar expansion", helperPath: "/opt/$HOME/broker", uid: 1000, wantErr: true},
		{name: "quote", helperPath: `/opt/"x"/broker`, uid: 1000, wantErr: true},
		{name: "backslash", helperPath: `/opt/a\b`, uid: 1000, wantErr: true},
		{name: "semicolon", helperPath: "/opt/a;b", uid: 1000, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := RenderSystemdUnit(tt.helperPath, tt.uid)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("RenderSystemdUnit() = %q, want error", got)
				}
				if got != nil {
					t.Errorf("RenderSystemdUnit() = %q on error, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("RenderSystemdUnit() error = %v", err)
			}
			if tt.want != "" && string(got) != tt.want {
				t.Errorf("RenderSystemdUnit() =\n%s\nwant\n%s", got, tt.want)
			}
			for _, s := range tt.wantSubs {
				if !strings.Contains(string(got), s) {
					t.Errorf("RenderSystemdUnit() = %q, missing %q", got, s)
				}
			}
		})
	}
}

func TestInstallStateRoundTrip(t *testing.T) {
	t.Parallel()

	full := InstallState{
		HostsLedger:       []byte("\n" + begin + "\n127.0.0.1 ws.jailoc.local\n::1 ws.jailoc.local\n" + end + "\n"),
		CACertSHA256:      strings.Repeat("ab", 32),
		CACertPEM:         []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"),
		SystemCACertPath:  "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt",
		SystemCAUpdateCmd: []string{"update-ca-certificates"},
		SystemCARemoveCmd: []string{"update-ca-certificates", "--fresh"},
		MacOSKeychainCert: true,
		NSSDatabases:      []string{"/home/u/.pki/nssdb", "/home/u/.mozilla/firefox/x.default"},
		HelperPath:        "/usr/local/libexec/jailoc-listener-broker",
		ServiceUnitPath:   "/etc/systemd/system/jailoc-listener-broker.service",
	}
	tests := []struct {
		name string
		st   InstallState
	}{
		{"zero", InstallState{}},
		{"full", full},
		{"ledger only", InstallState{HostsLedger: []byte("x\r\ny")}},
		{"keychain only", InstallState{MacOSKeychainCert: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "state", "install.json")
			if err := WriteInstallState(path, tt.st); err != nil {
				t.Fatalf("WriteInstallState() error = %v", err)
			}
			got, err := ReadInstallState(path)
			if err != nil {
				t.Fatalf("ReadInstallState() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.st) {
				t.Errorf("ReadInstallState() = %+v, want %+v", got, tt.st)
			}
		})
	}
}

func TestReadInstallStateMissing(t *testing.T) {
	t.Parallel()

	got, err := ReadInstallState(filepath.Join(t.TempDir(), "nope", "install.json"))
	if err != nil {
		t.Fatalf("ReadInstallState() error = %v, want nil for a missing file", err)
	}
	if !reflect.DeepEqual(got, InstallState{}) {
		t.Errorf("ReadInstallState() = %+v, want zero", got)
	}
}

func TestReadInstallStateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(t *testing.T, dir string) string
	}{
		{"invalid json", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "s.json")
			if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"empty file", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "s.json")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"wrong field type", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "s.json")
			if err := os.WriteFile(p, []byte(`{"nss_databases":"x"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}},
		{"path is a directory", func(t *testing.T, dir string) string { return dir }},
		{"parent is a file", func(t *testing.T, dir string) string {
			p := filepath.Join(dir, "f")
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return filepath.Join(p, "s.json")
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := tt.write(t, t.TempDir())
			got, err := ReadInstallState(path)
			if err == nil {
				t.Fatalf("ReadInstallState() = %+v, want error", got)
			}
			if !reflect.DeepEqual(got, InstallState{}) {
				t.Errorf("ReadInstallState() = %+v on error, want zero", got)
			}
		})
	}
}

func TestWriteInstallStateModes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	path := filepath.Join(dir, "install.json")
	if err := WriteInstallState(path, InstallState{HelperPath: "/h"}); err != nil {
		t.Fatalf("WriteInstallState() error = %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(file): %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	di, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat(dir): %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Errorf("directory mode = %o, want 700", got)
	}
}

func TestWriteInstallStateReplacesAtomically(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "install.json")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := InstallState{HelperPath: "/new"}
	if err := WriteInstallState(path, want); err != nil {
		t.Fatalf("WriteInstallState() error = %v", err)
	}
	got, err := ReadInstallState(path)
	if err != nil {
		t.Fatalf("ReadInstallState() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadInstallState() = %+v, want %+v", got, want)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("replaced file mode = %o, want 600", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after write, want only the state file", len(entries))
	}
}

func TestWriteInstallStateErrors(t *testing.T) {
	t.Parallel()

	t.Run("parent is a file", func(t *testing.T) {
		t.Parallel()
		f := filepath.Join(t.TempDir(), "f")
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteInstallState(filepath.Join(f, "s.json"), InstallState{}); err == nil {
			t.Fatal("WriteInstallState() error = nil, want error")
		}
	})
	t.Run("target is a directory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "s.json")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "keep"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := WriteInstallState(target, InstallState{}); err == nil {
			t.Fatal("WriteInstallState() error = nil, want error")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 {
			t.Errorf("directory holds %d entries after failed write, want the temporary file removed", len(entries))
		}
	})
}

func TestDetectLinuxCAStore(t *testing.T) {
	t.Parallel()

	debian := LinuxCAStore{
		CertPath:  "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt",
		UpdateCmd: []string{"update-ca-certificates"},
		RemoveCmd: []string{"update-ca-certificates", "--fresh"},
	}
	fedora := LinuxCAStore{
		CertPath:  "/etc/pki/ca-trust/source/anchors/jailoc-proxy-ca.crt",
		UpdateCmd: []string{"update-ca-trust"},
		RemoveCmd: []string{"update-ca-trust"},
	}
	arch := LinuxCAStore{
		CertPath:  "/etc/ca-certificates/trust-source/anchors/jailoc-proxy-ca.crt",
		UpdateCmd: []string{"trust", "extract-compat"},
		RemoveCmd: []string{"trust", "extract-compat"},
	}
	tests := []struct {
		name   string
		dirs   []string
		tools  []string
		want   LinuxCAStore
		wantOK bool
	}{
		{"debian", []string{"/usr/local/share/ca-certificates"}, []string{"update-ca-certificates"}, debian, true},
		{"fedora", []string{"/etc/pki/ca-trust/source/anchors"}, []string{"update-ca-trust"}, fedora, true},
		{"arch", []string{"/etc/ca-certificates/trust-source/anchors"}, []string{"trust"}, arch, true},
		{
			"debian wins over fedora",
			[]string{"/usr/local/share/ca-certificates", "/etc/pki/ca-trust/source/anchors"},
			[]string{"update-ca-certificates", "update-ca-trust"},
			debian, true,
		},
		{
			"fedora wins over arch",
			[]string{"/etc/pki/ca-trust/source/anchors", "/etc/ca-certificates/trust-source/anchors"},
			[]string{"update-ca-trust", "trust"},
			fedora, true,
		},
		{
			"debian dir without tool falls through to fedora",
			[]string{"/usr/local/share/ca-certificates", "/etc/pki/ca-trust/source/anchors"},
			[]string{"update-ca-trust"},
			fedora, true,
		},
		{"dir without tool", []string{"/usr/local/share/ca-certificates"}, nil, LinuxCAStore{}, false},
		{"tool without dir", nil, []string{"update-ca-certificates"}, LinuxCAStore{}, false},
		{"tool of another layout", []string{"/usr/local/share/ca-certificates"}, []string{"update-ca-trust"}, LinuxCAStore{}, false},
		{"nothing", nil, nil, LinuxCAStore{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dirExists := func(p string) bool {
				for _, d := range tt.dirs {
					if d == p {
						return true
					}
				}
				return false
			}
			lookPath := func(name string) (string, error) {
				for _, tool := range tt.tools {
					if tool == name {
						return "/usr/sbin/" + name, nil
					}
				}
				return "", errors.New("not found")
			}
			got, ok := DetectLinuxCAStore(dirExists, lookPath)
			if ok != tt.wantOK {
				t.Fatalf("DetectLinuxCAStore() ok = %v, want %v", ok, tt.wantOK)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DetectLinuxCAStore() = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("nil functions", func(t *testing.T) {
		t.Parallel()
		if got, ok := DetectLinuxCAStore(nil, nil); ok || !reflect.DeepEqual(got, LinuxCAStore{}) {
			t.Errorf("DetectLinuxCAStore(nil, nil) = %+v, %v; want zero, false", got, ok)
		}
	})
}

func nssTouch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

func TestDiscoverNSSDatabases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		goos  string
		files []string
		want  []string
	}{
		{name: "empty home linux", goos: "linux", want: []string{}},
		{name: "empty home darwin", goos: "darwin", want: []string{}},
		{
			name:  "linux chrome and firefox sorted",
			goos:  "linux",
			files: []string{".mozilla/firefox/b.default/cert9.db", ".pki/nssdb/cert9.db", ".mozilla/firefox/a.default/cert9.db"},
			want:  []string{".mozilla/firefox/a.default", ".mozilla/firefox/b.default", ".pki/nssdb"},
		},
		{
			name:  "linux snap and flatpak firefox",
			goos:  "linux",
			files: []string{"snap/firefox/common/.mozilla/firefox/s.default/cert9.db", ".var/app/org.mozilla.firefox/.mozilla/firefox/f.default/cert9.db"},
			want:  []string{".var/app/org.mozilla.firefox/.mozilla/firefox/f.default", "snap/firefox/common/.mozilla/firefox/s.default"},
		},
		{
			name:  "linux legacy chrome db",
			goos:  "linux",
			files: []string{".pki/nssdb/cert8.db"},
			want:  []string{".pki/nssdb"},
		},
		{
			name:  "linux nssdb dir without database",
			goos:  "linux",
			files: []string{".pki/nssdb/pkcs11.txt"},
			want:  []string{},
		},
		{
			name:  "firefox profile without cert9 and stray files ignored",
			goos:  "linux",
			files: []string{".mozilla/firefox/x.default/cert8.db", ".mozilla/firefox/x.default/key4.db", ".mozilla/firefox/profiles.ini", ".mozilla/firefox/Crash Reports/foo"},
			want:  []string{},
		},
		{
			name:  "darwin firefox profiles",
			goos:  "darwin",
			files: []string{"Library/Application Support/Firefox/Profiles/z.default/cert9.db", "Library/Application Support/Firefox/Profiles/a.default-release/cert9.db", "Library/Application Support/Firefox/Profiles/none/places.sqlite"},
			want:  []string{"Library/Application Support/Firefox/Profiles/a.default-release", "Library/Application Support/Firefox/Profiles/z.default"},
		},
		{
			name:  "darwin ignores linux locations",
			goos:  "darwin",
			files: []string{".pki/nssdb/cert9.db", ".mozilla/firefox/a.default/cert9.db"},
			want:  []string{},
		},
		{
			name:  "linux ignores darwin locations",
			goos:  "linux",
			files: []string{"Library/Application Support/Firefox/Profiles/a.default/cert9.db"},
			want:  []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			for _, f := range tt.files {
				nssTouch(t, filepath.Join(home, filepath.FromSlash(f)))
			}
			want := make([]string, len(tt.want))
			for i, w := range tt.want {
				want[i] = filepath.Join(home, filepath.FromSlash(w))
			}
			got, err := DiscoverNSSDatabases(tt.goos, home)
			if err != nil {
				t.Fatalf("DiscoverNSSDatabases() error = %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("DiscoverNSSDatabases() = %q, want %q", got, want)
			}
		})
	}
}

func TestDiscoverNSSDatabasesFollowsSymlinkedProfile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	real := filepath.Join(home, "elsewhere", "p.default")
	nssTouch(t, filepath.Join(real, "cert9.db"))
	link := filepath.Join(home, ".mozilla", "firefox", "link.default")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := DiscoverNSSDatabases("linux", home)
	if err != nil {
		t.Fatalf("DiscoverNSSDatabases() error = %v", err)
	}
	if want := []string{link}; !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverNSSDatabases() = %q, want %q", got, want)
	}
}

func TestDiscoverNSSDatabasesGlobCharactersInHome(t *testing.T) {
	t.Parallel()

	home := filepath.Join(t.TempDir(), "h[ome]*?")
	nssTouch(t, filepath.Join(home, ".mozilla", "firefox", "a.default", "cert9.db"))
	got, err := DiscoverNSSDatabases("linux", home)
	if err != nil {
		t.Fatalf("DiscoverNSSDatabases() error = %v", err)
	}
	if want := []string{filepath.Join(home, ".mozilla", "firefox", "a.default")}; !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverNSSDatabases() = %q, want %q", got, want)
	}
}

func TestDiscoverNSSDatabasesErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		goos string
		home string
	}{
		{"unsupported os", "windows", t.TempDir()},
		{"empty os", "", t.TempDir()},
		{"empty home", "linux", ""},
		{"relative home", "darwin", "home/u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := DiscoverNSSDatabases(tt.goos, tt.home)
			if err == nil {
				t.Fatalf("DiscoverNSSDatabases() = %q, want error", got)
			}
			if got != nil {
				t.Errorf("DiscoverNSSDatabases() = %q on error, want nil", got)
			}
		})
	}

	t.Run("profile root is a file", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		nssTouch(t, filepath.Join(home, ".mozilla", "firefox"))
		got, err := DiscoverNSSDatabases("linux", home)
		if err != nil {
			t.Fatalf("DiscoverNSSDatabases() error = %v, want the file root skipped", err)
		}
		if len(got) != 0 {
			t.Errorf("DiscoverNSSDatabases() = %q, want none", got)
		}
	})
}

func TestProbeBroker(t *testing.T) {
	t.Parallel()

	t.Run("idle broker", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		if err := ProbeBroker(ctx, h.path, brkUID(), h.addrs[0]); err != nil {
			t.Fatalf("ProbeBroker() error = %v", err)
		}
		// The probe detaches again, so a real gateway can attach.
		gw := brkAttach(t, ctx, h, h.addrs[0])
		brkServe(t, gw, "A")
		brkWantServed(t, h.addrs[0], "A")
	})

	t.Run("busy broker", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		gw, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
		if err != nil {
			t.Fatalf("AttachBroker: %v", err)
		}
		brkServe(t, gw, "A")
		if err := ProbeBroker(ctx, h.path, brkUID(), h.addrs[0]); err != nil {
			t.Fatalf("ProbeBroker() with an attached gateway error = %v, want nil", err)
		}
		// The probe must not disturb the attached gateway.
		brkWantServed(t, h.addrs[0], "A")
	})

	t.Run("wrong broker uid", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		err := ProbeBroker(ctx, h.path, brkUID()+1, h.addrs[0])
		if err == nil {
			t.Fatal("ProbeBroker() error = nil for a broker running as another UID")
		}
		if errors.Is(err, ErrBrokerBusy) {
			t.Errorf("ProbeBroker() = %v, want a UID refusal", err)
		}
	})

	t.Run("wrong broker uid while busy", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		gw, err := AttachBroker(ctx, h.path, brkUID(), h.addrs[0])
		if err != nil {
			t.Fatalf("AttachBroker: %v", err)
		}
		brkServe(t, gw, "A")
		if err := ProbeBroker(ctx, h.path, brkUID()+1, h.addrs[0]); err == nil {
			t.Fatal("ProbeBroker() error = nil: a busy broker of another UID must not count as running")
		}
	})

	t.Run("refused client uid", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID() + 1})
		if err := ProbeBroker(ctx, h.path, brkUID(), h.addrs[0]); err == nil {
			t.Fatal("ProbeBroker() error = nil, want the broker to refuse this client")
		}
	})

	t.Run("missing socket", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 443}
		err := ProbeBroker(ctx, filepath.Join(brkDir(t), "missing.sock"), brkUID(), addr)
		if err == nil {
			t.Fatal("ProbeBroker() error = nil for a missing socket")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("ProbeBroker() = %v, want an error wrapping os.ErrNotExist", err)
		}
	})

	t.Run("invalid address", func(t *testing.T) {
		t.Parallel()
		ctx := brkContext(t)
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		for _, addr := range []*net.TCPAddr{nil, {IP: net.IPv4(10, 0, 0, 1), Port: 443}, {IP: net.IPv4(127, 0, 0, 1)}} {
			if err := ProbeBroker(ctx, h.path, brkUID(), addr); err == nil {
				t.Errorf("ProbeBroker(%v) error = nil, want error", addr)
			}
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(brkContext(t))
		cancel()
		h := brkStart(t, BrokerConfig{OwnerUID: brkUID()})
		if err := ProbeBroker(ctx, h.path, brkUID(), h.addrs[0]); err == nil {
			t.Fatal("ProbeBroker() error = nil with a canceled context")
		}
	})
}
