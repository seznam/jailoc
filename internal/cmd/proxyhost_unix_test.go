//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/seznam/jailoc/internal/workspace"
)

const (
	hostTestUID      = 501
	hostTestOriginal = "127.0.0.1 localhost\n::1 localhost\n"
	hostTestConfig   = "[workspaces.app.proxy]\napp = 3000\napi = 8080\n\n[workspaces.plain]\n"
	hostTestBroker   = "#!/bin/sh\n# stub listener broker\n"
)

// privCall is one runPrivileged call with the files it would have installed
// read while they still exist.
type privCall struct {
	script string
	env    map[string]string
	files  map[string][]byte // by env key (JAILOC_*_SRC)
}

// hostHarness replaces currentProxyHostDeps with stubs: nothing runs sudo,
// launchctl, systemctl, security or certutil, and /etc/hosts is a file in a
// temporary home.
type hostHarness struct {
	t         *testing.T
	home      string
	hostsPath string
	goos      string

	priv     []privCall
	commands [][]string
	deps     *proxyHostDeps

	privErr      error
	commandErr   func(args []string) error
	answers      string
	stdinIsTTY   bool
	certutil     bool
	systemStore  bool
	brokerAnswer error
}

// useHostHarness prepares a temporary HOME with config text cfgTOML and a
// hosts file with content hosts, and installs the stubbed deps for goos.
// Tests using it must not be parallel.
func useHostHarness(t *testing.T, goos, cfgTOML, hosts string) *hostHarness {
	t.Helper()
	home := shortTempDir(t, "ph")
	t.Setenv("HOME", home)
	t.Setenv("TERM", "xterm")
	t.Setenv("ACCESSIBLE", "")
	cfgPath := filepath.Join(home, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.PathEnv, cfgPath)
	hostsPath := filepath.Join(home, "hosts")
	if err := os.WriteFile(hostsPath, []byte(hosts), 0o644); err != nil { //nolint:gosec // test hosts file
		t.Fatal(err)
	}
	brokerPath := filepath.Join(home, "jailoc-listener-broker")
	if err := os.WriteFile(brokerPath, []byte(hostTestBroker), 0o700); err != nil { //nolint:gosec // test stub binary
		t.Fatal(err)
	}

	h := &hostHarness{t: t, home: home, hostsPath: hostsPath, goos: goos, stdinIsTTY: true, certutil: true, systemStore: true}
	old := currentProxyHostDeps
	currentProxyHostDeps = proxyHostDeps{
		goos:         goos,
		uid:          func() uint32 { return hostTestUID },
		homeDir:      func() (string, error) { return home, nil },
		hostsPath:    hostsPath,
		brokerBinary: func() (string, func(), error) { return brokerPath, func() {}, nil },
		dirExists: func(p string) bool {
			return h.systemStore && p == "/usr/local/share/ca-certificates"
		},
		lookPath: func(name string) (string, error) {
			switch {
			case name == "certutil" && h.certutil:
				return "/usr/bin/certutil", nil
			case name == "update-ca-certificates" && h.systemStore:
				return "/usr/sbin/update-ca-certificates", nil
			}
			return "", errors.New("not found")
		},
		runPrivileged: h.runPrivileged,
		runCommand: func(_ context.Context, name string, args ...string) error {
			call := append([]string{name}, args...)
			h.commands = append(h.commands, call)
			if h.commandErr != nil {
				return h.commandErr(call)
			}
			return nil
		},
		probeBroker: func(context.Context) error { return h.brokerAnswer },
		interactive: func() bool { return h.stdinIsTTY },
		prompt: func(yes bool) *promptIO {
			return &promptIO{in: strings.NewReader(h.answers), out: io.Discard, accessible: true, yes: yes}
		},
	}
	t.Cleanup(func() { currentProxyHostDeps = old })
	return h
}

// runPrivileged records the call and applies its effect on the hosts file,
// like the real script does.
func (h *hostHarness) runPrivileged(_ context.Context, script string, env []string) error {
	call := privCall{script: script, env: map[string]string{}, files: map[string][]byte{}}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		call.env[k] = v
	}
	for k, v := range call.env {
		if !strings.HasSuffix(k, "_SRC") {
			continue
		}
		data, err := os.ReadFile(v)
		if err != nil {
			h.t.Errorf("staged %s unreadable: %v", k, err)
		}
		call.files[k] = data
		sum := sha256.Sum256(data)
		if want := call.env[strings.TrimSuffix(k, "_SRC")+"_SHA256"]; hex.EncodeToString(sum[:]) != want {
			h.t.Errorf("%s checksum = %x, env says %s", k, sum, want)
		}
	}
	h.priv = append(h.priv, call)
	if h.privErr != nil {
		return h.privErr
	}
	if src, ok := call.files["JAILOC_HOSTS_SRC"]; ok {
		if dst := call.env["JAILOC_HOSTS_DST"]; dst != h.hostsPath {
			h.t.Errorf("JAILOC_HOSTS_DST = %q, want %q", dst, h.hostsPath)
		}
		if err := os.WriteFile(h.hostsPath, src, 0o644); err != nil { //nolint:gosec // test hosts file
			h.t.Fatal(err)
		}
	}
	return nil
}

func (h *hostHarness) run(args ...string) (string, error) {
	h.t.Helper()
	cmd := newProxyCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(h.t.Context())
	return out.String(), err
}

func (h *hostHarness) hosts() string {
	h.t.Helper()
	data, err := os.ReadFile(h.hostsPath)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(data)
}

func (h *hostHarness) installState() proxyhost.InstallState {
	h.t.Helper()
	st, err := proxyhost.ReadInstallState(proxyInstallStatePath())
	if err != nil {
		h.t.Fatal(err)
	}
	return st
}

func (h *hostHarness) addNSSDatabase(rel string) string {
	h.t.Helper()
	dir := filepath.Join(h.home, rel)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cert9.db"), nil, 0o600); err != nil {
		h.t.Fatal(err)
	}
	return dir
}

func (h *hostHarness) hasCommand(prefix ...string) bool {
	return slices.ContainsFunc(h.commands, func(c []string) bool {
		return len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix)
	})
}

func (h *hostHarness) commandIndex(prefix ...string) int {
	return slices.IndexFunc(h.commands, func(c []string) bool {
		return len(c) >= len(prefix) && slices.Equal(c[:len(prefix)], prefix)
	})
}

func readSavedProxyOptions(t *testing.T) proxySavedOptions {
	t.Helper()
	data, err := os.ReadFile(proxyOptionsFile())
	if err != nil {
		t.Fatalf("read options.json: %v", err)
	}
	var saved proxySavedOptions
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	return saved
}

func wantHostRecords(t *testing.T, hosts string, names ...string) {
	t.Helper()
	for _, n := range names {
		for _, ip := range []string{"127.0.0.1", "::1"} {
			if !strings.Contains(hosts, ip+" "+n+"\n") {
				t.Errorf("hosts lacks %q:\n%s", ip+" "+n, hosts)
			}
		}
	}
}

func TestDesiredProxyHostNames(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Workspaces: map[string]config.Workspace{
		"zeta":  {Proxy: config.ProxyMap(map[string]int{"web": 80})},
		"alpha": {Proxy: config.ProxyMap(map[string]int{"app": 3000, "api": 8080})},
		"plain": {},
	}}
	got, err := desiredProxyHostNames(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []proxyhost.Name{
		{Workspace: "alpha"}, {Workspace: "alpha", Route: "api"}, {Workspace: "alpha", Route: "app"},
		{Workspace: "zeta"}, {Workspace: "zeta", Route: "web"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("names = %v, want %v", got, want)
	}
	if got, err := desiredProxyHostNames(&config.Config{}); err != nil || len(got) != 0 {
		t.Errorf("empty config: %v, %v", got, err)
	}
}

func TestProxyInstallDarwin(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	ffDB := h.addNSSDatabase("Library/Application Support/Firefox/Profiles/abc.default")

	out, err := h.run("install", "--yes")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}

	hosts := h.hosts()
	if !strings.HasPrefix(hosts, hostTestOriginal) {
		t.Errorf("original hosts lines were not preserved:\n%s", hosts)
	}
	wantHostRecords(t, hosts, "app.jailoc.local", "api.app.jailoc.local", "app.app.jailoc.local")
	if strings.Contains(hosts, "plain.jailoc.local") {
		t.Errorf("a workspace without routes got host names:\n%s", hosts)
	}

	if len(h.priv) != 1 {
		t.Fatalf("privileged calls = %d, want 1", len(h.priv))
	}
	call := h.priv[0]
	for _, want := range []string{"launchctl bootstrap system", "launchctl kickstart -k", `write_hosts "$`, "install_file 0755 \"$", "install_file 0644 \"$"} {
		if !strings.Contains(call.script, want) {
			t.Errorf("script lacks %q:\n%s", want, call.script)
		}
	}
	for _, bad := range []string{"systemctl", "update-ca-certificates", h.home} {
		if strings.Contains(call.script, bad) {
			t.Errorf("script must be constant text and macOS-only, found %q", bad)
		}
	}
	if call.env["JAILOC_HELPER_DST"] != "/Library/PrivilegedHelperTools/jailoc-listener-broker" ||
		call.env["JAILOC_UNIT_DST"] != "/Library/LaunchDaemons/cz.seznam.jailoc.listener-broker.plist" {
		t.Errorf("install destinations = %q, %q", call.env["JAILOC_HELPER_DST"], call.env["JAILOC_UNIT_DST"])
	}
	if string(call.files["JAILOC_HELPER_SRC"]) != hostTestBroker {
		t.Errorf("staged helper = %q", call.files["JAILOC_HELPER_SRC"])
	}
	unit := string(call.files["JAILOC_UNIT_SRC"])
	for _, want := range []string{"<string>/Library/PrivilegedHelperTools/jailoc-listener-broker</string>", "<string>--uid</string>", "<string>501</string>"} {
		if !strings.Contains(unit, want) {
			t.Errorf("plist lacks %q:\n%s", want, unit)
		}
	}
	if _, ok := call.env["JAILOC_CA_DST"]; ok {
		t.Error("macOS must not install into a Linux system store")
	}

	caPath := proxyCACertPath()
	keychain := filepath.Join(h.home, "Library", "Keychains", "login.keychain-db")
	if !h.hasCommand("security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, caPath) {
		t.Errorf("no keychain trust command in %v", h.commands)
	}
	if !h.hasCommand("certutil", "-d", "sql:"+ffDB, "-A", "-t", "C,,", "-n", "jailoc proxy CA", "-i", caPath) {
		t.Errorf("no certutil add for %s in %v", ffDB, h.commands)
	}
	if h.commandIndex("certutil", "-d", "sql:"+ffDB, "-D") > h.commandIndex("certutil", "-d", "sql:"+ffDB, "-A") {
		t.Error("the old NSS entry must be deleted before the new one is added")
	}

	st := h.installState()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(st.CACertPEM, caPEM) || st.CACertSHA256 == "" || !st.MacOSKeychainCert {
		t.Errorf("state = %+v, want the CA recorded as trusted in the keychain", st)
	}
	if !bytes.Contains([]byte(hosts), st.HostsLedger) || len(st.HostsLedger) == 0 {
		t.Errorf("ledger %q is not the block in the hosts file", st.HostsLedger)
	}
	if !slices.Equal(st.NSSDatabases, []string{ffDB}) || st.HelperPath != call.env["JAILOC_HELPER_DST"] || st.ServiceUnitPath != call.env["JAILOC_UNIT_DST"] {
		t.Errorf("state = %+v", st)
	}
	if saved := readSavedProxyOptions(t); !saved.ListenerBroker {
		t.Errorf("options.json = %+v, want listener_broker", saved)
	}
	info, err := os.Stat(proxyInstallStatePath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("install.json mode = %v, %v; want 0600", info, err)
	}
	assertContains(t, out, "Listener broker is running")

	// Nothing changed: a second install leaves /etc/hosts alone.
	h.priv, h.commands = nil, nil
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	if h.hosts() != hosts {
		t.Error("an unchanged install rewrote /etc/hosts")
	}
	if strings.Contains(h.priv[0].script, `write_hosts "$`) || h.priv[0].env["JAILOC_HOSTS_SRC"] != "" {
		t.Errorf("an unchanged install must not touch /etc/hosts:\n%s", h.priv[0].script)
	}
	if h.hasCommand("security", "delete-certificate") {
		t.Error("the CA did not change; it must not be removed from the keychain")
	}
}

func TestProxyInstallLinux(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	chromeDB := h.addNSSDatabase(".pki/nssdb")
	ffDB := h.addNSSDatabase(".mozilla/firefox/abc.default")

	out, err := h.run("install", "-y")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	wantHostRecords(t, h.hosts(), "app.jailoc.local", "api.app.jailoc.local", "app.app.jailoc.local")
	if len(h.priv) != 1 {
		t.Fatalf("privileged calls = %d, want 1", len(h.priv))
	}
	call := h.priv[0]
	for _, want := range []string{"systemctl daemon-reload", "systemctl enable", "systemctl restart", "'update-ca-certificates'", `write_hosts "$`} {
		if !strings.Contains(call.script, want) {
			t.Errorf("script lacks %q:\n%s", want, call.script)
		}
	}
	if strings.Contains(call.script, "launchctl") {
		t.Error("Linux script mentions launchctl")
	}
	if call.env["JAILOC_HELPER_DST"] != "/usr/local/libexec/jailoc-listener-broker" || call.env["JAILOC_UNIT_DST"] != "/etc/systemd/system/jailoc-listener-broker.service" {
		t.Errorf("destinations = %q, %q", call.env["JAILOC_HELPER_DST"], call.env["JAILOC_UNIT_DST"])
	}
	if !strings.Contains(string(call.files["JAILOC_UNIT_SRC"]), "ExecStart=/usr/local/libexec/jailoc-listener-broker --uid 501\n") {
		t.Errorf("unit = %s", call.files["JAILOC_UNIT_SRC"])
	}
	caPEM, err := os.ReadFile(proxyCACertPath())
	if err != nil {
		t.Fatal(err)
	}
	if call.env["JAILOC_CA_DST"] != "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt" || !bytes.Equal(call.files["JAILOC_CA_SRC"], caPEM) {
		t.Errorf("system CA install: dst %q, src matches ca.crt: %v", call.env["JAILOC_CA_DST"], bytes.Equal(call.files["JAILOC_CA_SRC"], caPEM))
	}
	if h.hasCommand("security") {
		t.Error("Linux must not call security")
	}
	for _, db := range []string{chromeDB, ffDB} {
		if !h.hasCommand("certutil", "-d", "sql:"+db, "-A", "-t", "C,,", "-n", "jailoc proxy CA", "-i", proxyCACertPath()) {
			t.Errorf("no certutil add for %s in %v", db, h.commands)
		}
	}
	st := h.installState()
	if st.SystemCACertPath != call.env["JAILOC_CA_DST"] || !slices.Equal(st.SystemCARemoveCmd, []string{"update-ca-certificates", "--fresh"}) ||
		!slices.Equal(st.NSSDatabases, []string{ffDB, chromeDB}) || st.MacOSKeychainCert {
		t.Errorf("state = %+v", st)
	}
	if strings.Contains(out, "chrome://settings/certificates") {
		t.Errorf("Chrome's database was configured; no manual note expected:\n%s", out)
	}
}

func TestProxyInstallManualTrustInstructions(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.certutil, h.systemStore = false, false
	ffDB := h.addNSSDatabase(".mozilla/firefox/abc.default")

	out, err := h.run("install", "--yes")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	call := h.priv[0]
	if _, ok := call.env["JAILOC_CA_DST"]; ok || strings.Contains(call.script, "update-ca") {
		t.Errorf("no system store was found, nothing should be installed there:\n%s", call.script)
	}
	if len(h.commands) != 0 {
		t.Errorf("commands = %v, want none without certutil", h.commands)
	}
	for _, want := range []string{"no supported Linux system trust store", "certutil", ffDB, "chrome://settings/certificates"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks manual instruction %q:\n%s", want, out)
		}
	}
	if st := h.installState(); st.SystemCACertPath != "" || len(st.NSSDatabases) != 0 {
		t.Errorf("state records trust that was not configured: %+v", st)
	}
}

func TestProxyInstallRotatesCAAndUpdatesHosts(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	first := h.installState()
	oldSHA1 := pemSHA1(first.CACertPEM)
	if oldSHA1 == "" {
		t.Fatal("no SHA-1 for the first CA")
	}

	// A new CA and a new route.
	for _, f := range []string{"ca.crt", "intermediate.crt", "intermediate.key", "cert.crt", "cert.key"} {
		if err := os.Remove(filepath.Join(proxyCADir(), f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	cfgTOML := hostTestConfig + "\n[workspaces.desk.proxy]\nweb = 6080\n"
	if err := os.WriteFile(config.ConfigPath(), []byte(cfgTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	h.priv, h.commands = nil, nil

	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("second install: %v\n%s", err, out)
	}
	second := h.installState()
	if second.CACertSHA256 == first.CACertSHA256 {
		t.Fatal("the CA was not rotated")
	}
	del := h.commandIndex("security", "delete-certificate", "-Z", oldSHA1)
	add := h.commandIndex("security", "add-trusted-cert")
	if del < 0 || add < 0 || del > add {
		t.Errorf("want the old CA (%s) deleted before the new one is trusted, got %v", oldSHA1, h.commands)
	}
	wantHostRecords(t, h.hosts(), "desk.jailoc.local", "web.desk.jailoc.local", "app.jailoc.local")
	if strings.Count(h.hosts(), "# BEGIN jailoc proxy hosts") != 1 || !strings.HasPrefix(h.hosts(), hostTestOriginal) {
		t.Errorf("hosts block was not replaced in place:\n%s", h.hosts())
	}
	if !bytes.Equal(second.HostsLedger, []byte(h.hosts()[len(hostTestOriginal):])) {
		t.Errorf("ledger %q does not match the new block", second.HostsLedger)
	}
}

func TestProxyInstallReplacesSavedOptions(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	// A saved non-broker options file stands for the earlier setup; install
	// replaces it.
	if err := writeProxySavedOptions(proxyOptions{listen: proxyDefaultListen}); err != nil {
		t.Fatal(err)
	}
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if saved := readSavedProxyOptions(t); !saved.ListenerBroker || saved.Listen != "" {
		t.Errorf("options.json = %+v, want only listener_broker", saved)
	}
}

func TestProxyInstallAbortsOnNo(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.answers = "n\n"

	out, err := h.run("install")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	assertContains(t, out, "Aborted.")
	if len(h.priv) != 0 || len(h.commands) != 0 {
		t.Errorf("an aborted install ran %d privileged and %d user commands", len(h.priv), len(h.commands))
	}
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts changed:\n%s", h.hosts())
	}
	for _, p := range []string{proxyInstallStatePath(), proxyOptionsFile()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after an aborted install (%v)", p, err)
		}
	}
}

func TestProxyInstallShowsSummaryBeforeAsking(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	var shown bytes.Buffer
	currentProxyHostDeps.prompt = func(bool) *promptIO {
		return &promptIO{in: strings.NewReader("n\n"), out: &shown, accessible: true}
	}
	if _, err := h.run("install"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Install host support for jailoc proxy?",
		"/usr/local/libexec/jailoc-listener-broker",
		"/etc/systemd/system/jailoc-listener-broker.service",
		h.hostsPath,
		"/usr/local/share/ca-certificates/jailoc-proxy-ca.crt",
	} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("prompt lacks %q:\n%s", want, shown.String())
		}
	}
}

func TestProxyInstallAndUninstallRefuseRoot(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	currentProxyHostDeps.uid = func() uint32 { return 0 }
	for _, verb := range []string{"install", "uninstall"} {
		_, err := h.run(verb, "--yes")
		if err == nil || !strings.Contains(err.Error(), "refusing to run proxy "+verb+" as root; run it as your own user") {
			t.Errorf("%s as root: error = %v", verb, err)
		}
	}
	if len(h.priv) != 0 || len(h.commands) != 0 || h.hosts() != hostTestOriginal {
		t.Error("a refused run changed the host")
	}
}

func TestProxyInstallNeedsTerminalOrYes(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.stdinIsTTY = false
	for _, verb := range []string{"install", "uninstall"} {
		_, err := h.run(verb)
		if err == nil || !strings.Contains(err.Error(), "proxy "+verb+" asks for confirmation; run it in a terminal or pass --yes") {
			t.Errorf("%s without a terminal: error = %v", verb, err)
		}
	}
	t.Setenv("ACCESSIBLE", "1")
	h.answers = "n\n"
	if out, err := h.run("install"); err != nil || !strings.Contains(out, "Aborted.") {
		t.Errorf("accessible mode must be allowed to ask: %v\n%s", err, out)
	}
}

func TestProxyInstallFailureLeavesNoState(t *testing.T) {
	t.Run("sudo fails", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.privErr = errors.New("sudo: a password is required")
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "sudo: a password is required") {
			t.Fatalf("error = %v", err)
		}
		if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install.json written after a failed privileged step (%v)", err)
		}
		if len(h.commands) != 0 {
			t.Errorf("trust commands ran after a failed privileged step: %v", h.commands)
		}
	})
	t.Run("host name defined elsewhere", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal+"127.0.0.1 app.jailoc.local\n")
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "already defined outside the managed block") {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 {
			t.Error("a conflicting hosts file reached the privileged step")
		}
	})
	t.Run("keychain refuses", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.commandErr = func(args []string) error {
			if args[0] == "security" {
				return errors.New("user canceled")
			}
			return nil
		}
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "login keychain") {
			t.Fatalf("error = %v", err)
		}
		// The privileged part happened, so uninstall can still revert it.
		if st := h.installState(); len(st.HostsLedger) == 0 || st.MacOSKeychainCert {
			t.Errorf("state = %+v, want the hosts ledger and no keychain trust", st)
		}
	})
	t.Run("no broker binary", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		currentProxyHostDeps.brokerBinary = func() (string, func(), error) { return "", nil, errors.New("no broker in this build") }
		if _, err := h.run("install", "--yes"); err == nil || !strings.Contains(err.Error(), "no broker in this build") {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 {
			t.Error("privileged step ran without a broker binary")
		}
	})
}

func TestProxyUninstallDarwin(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	ffDB := h.addNSSDatabase("Library/Application Support/Firefox/Profiles/abc.default")
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	st := h.installState()
	wantSHA1 := pemSHA1(st.CACertPEM)
	h.priv, h.commands = nil, nil

	out, err := h.run("uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts not restored byte for byte:\n%q", h.hosts())
	}
	for _, p := range []string{proxyInstallStatePath(), proxyOptionsFile()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists (%v)", p, err)
		}
	}
	if len(h.priv) != 1 {
		t.Fatalf("privileged calls = %d, want 1", len(h.priv))
	}
	call := h.priv[0]
	for _, want := range []string{`launchctl bootout "system/$JAILOC_SERVICE_LABEL"`, `rm -f "$JAILOC_UNIT_DST"`, `rm -f "$JAILOC_HELPER_DST"`, `rm -rf "$JAILOC_SOCKET_DIR"`, `write_hosts "$`} {
		if !strings.Contains(call.script, want) {
			t.Errorf("script lacks %q:\n%s", want, call.script)
		}
	}
	if call.env["JAILOC_SOCKET_DIR"] != "/var/run/jailoc-proxy/501" || call.env["JAILOC_HELPER_DST"] != "/Library/PrivilegedHelperTools/jailoc-listener-broker" {
		t.Errorf("env = %v", call.env)
	}
	keychain := filepath.Join(h.home, "Library", "Keychains", "login.keychain-db")
	if !h.hasCommand("security", "delete-certificate", "-Z", wantSHA1, "-t", keychain) {
		t.Errorf("no keychain removal in %v", h.commands)
	}
	if !h.hasCommand("certutil", "-d", "sql:"+ffDB, "-D", "-n", "jailoc proxy CA") {
		t.Errorf("no NSS removal in %v", h.commands)
	}
	if _, err := os.Stat(proxyCACertPath()); err != nil {
		t.Errorf("the CA files must be kept: %v", err)
	}
}

func TestProxyUninstallLinux(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	chromeDB := h.addNSSDatabase(".pki/nssdb")
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	h.priv, h.commands = nil, nil

	out, err := h.run("uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts not restored byte for byte:\n%q", h.hosts())
	}
	call := h.priv[0]
	for _, want := range []string{`systemctl disable --now "$JAILOC_SERVICE_NAME"`, "systemctl daemon-reload", `rm -f "$JAILOC_CA_DST"`, "'update-ca-certificates' '--fresh'"} {
		if !strings.Contains(call.script, want) {
			t.Errorf("script lacks %q:\n%s", want, call.script)
		}
	}
	if call.env["JAILOC_CA_DST"] != "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt" {
		t.Errorf("JAILOC_CA_DST = %q", call.env["JAILOC_CA_DST"])
	}
	if !h.hasCommand("certutil", "-d", "sql:"+chromeDB, "-D") || h.hasCommand("security") {
		t.Errorf("commands = %v", h.commands)
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

func TestProxyUninstallPreservesForeignHostsEdits(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	// Someone appended a line after our block.
	if err := os.WriteFile(h.hostsPath, []byte(h.hosts()+"10.0.0.1 corp.example\n"), 0o644); err != nil { //nolint:gosec // test hosts file
		t.Fatal(err)
	}
	if out, err := h.run("uninstall", "--yes"); err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if want := hostTestOriginal + "10.0.0.1 corp.example\n"; h.hosts() != want {
		t.Errorf("hosts = %q, want %q", h.hosts(), want)
	}
}

func TestProxyUninstallRefusesEditedBlock(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	edited := strings.Replace(h.hosts(), "127.0.0.1 api.app.jailoc.local\n", "127.0.0.1 api.app.jailoc.local # mine\n", 1)
	if err := os.WriteFile(h.hostsPath, []byte(edited), 0o644); err != nil { //nolint:gosec // test hosts file
		t.Fatal(err)
	}
	h.priv = nil
	_, err := h.run("uninstall", "--yes")
	if err == nil || !strings.Contains(err.Error(), "revert") {
		t.Fatalf("error = %v, want a refusal to revert an edited block", err)
	}
	if len(h.priv) != 0 {
		t.Error("the privileged step ran although the hosts block could not be reverted")
	}
	if _, err := os.Stat(proxyInstallStatePath()); err != nil {
		t.Errorf("install.json must stay so uninstall can be retried: %v", err)
	}
}

func TestProxyUninstallIgnoresTamperedSystemCAState(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	st := h.installState()
	st.SystemCACertPath = "/etc/passwd"
	st.SystemCARemoveCmd = []string{"touch", "/etc/pwned"}
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	h.priv = nil
	out, err := h.run("uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	call := h.priv[0]
	if strings.Contains(call.script, "pwned") || strings.Contains(call.script, "JAILOC_CA_DST") {
		t.Errorf("a tampered install.json reached the root script:\n%s", call.script)
	}
	for k, v := range call.env {
		if strings.Contains(v, "passwd") || strings.Contains(v, "pwned") {
			t.Errorf("env %s=%s from a tampered install.json", k, v)
		}
	}
	assertContains(t, out, "ignoring the system CA path")
}

func TestProxyUninstallAbortsOnNo(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	hosts := h.hosts()
	h.priv, h.commands = nil, nil
	h.answers = "n\n"
	out, err := h.run("uninstall")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	assertContains(t, out, "Aborted.")
	if len(h.priv) != 0 || len(h.commands) != 0 || h.hosts() != hosts {
		t.Error("an aborted uninstall changed the host")
	}
	if _, err := os.Stat(proxyInstallStatePath()); err != nil {
		t.Errorf("install.json removed by an aborted uninstall: %v", err)
	}
}

func TestProxyUninstallWithoutInstall(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	out, err := h.run("uninstall", "--yes")
	if err != nil {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts changed:\n%q", h.hosts())
	}
	if len(h.priv) != 1 || strings.Contains(h.priv[0].script, `write_hosts "$`) {
		t.Errorf("privileged calls = %+v, want only the service cleanup", h.priv)
	}
}

func TestProxyStatusReportsHostSetup(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.brokerAnswer = errors.New("connection refused")

	out, err := h.run("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Proxy is not running",
		"Listener broker: not running",
		"CA certificate: not created yet",
		"Hosts (" + h.hostsPath + "): missing api.app.jailoc.local, app.app.jailoc.local, app.jailoc.local;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	h.brokerAnswer = nil
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	out, err = h.run("status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	for _, want := range []string{
		"Listener broker: running",
		"CA certificate: ",
		"trusted by `jailoc proxy install`",
		"Hosts (" + h.hostsPath + "): all 3 proxy host names present",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "missing") {
		t.Errorf("status reports missing names after install:\n%s", out)
	}

	// A CA that changed since install is reported.
	st := h.installState()
	st.CACertSHA256 = strings.Repeat("0", 64)
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	if out, _ := h.run("status"); !strings.Contains(out, "changed since install") {
		t.Errorf("status = %s", out)
	}
}

func TestProxyStatusHostsPartiallyMissing(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal+"127.0.0.1 app.jailoc.local\n::1 app.jailoc.local\n")
	out, err := h.run("status")
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, out, "missing api.app.jailoc.local, app.app.jailoc.local;")
	if strings.Contains(out, "missing app.jailoc.local") {
		t.Errorf("a present name is reported missing:\n%s", out)
	}

	if err := os.WriteFile(config.ConfigPath(), []byte("[workspaces.plain]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ = h.run("status")
	assertContains(t, out, "nothing needed")
}

func TestProxyCommandListsHostSubcommands(t *testing.T) {
	t.Parallel()
	cmd := newProxyCmd()
	if cmd.Hidden {
		t.Error("jailoc proxy is hidden")
	}
	var names []string
	for _, c := range cmd.Commands() {
		names = append(names, c.Name())
		if c.Name() == "install" || c.Name() == "uninstall" {
			if c.Flags().Lookup("yes") == nil || c.Flags().ShorthandLookup("y") == nil {
				t.Errorf("%s lacks --yes/-y", c.Name())
			}
		}
	}
	slices.Sort(names)
	if want := []string{"install", "status", "stop", "uninstall"}; !slices.Equal(names, want) {
		t.Errorf("subcommands = %v, want %v", names, want)
	}
	for _, want := range []string{"jailoc proxy install", "jailoc proxy uninstall", "jailoc proxy status", "jailoc proxy stop"} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("Long help lacks %q", want)
		}
	}
}

func TestWarnMissingProxyHosts(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal+"127.0.0.1 app.jailoc.local\n")
	ws := mustResolveHostTestWorkspace(t, "app")

	var errw bytes.Buffer
	warnMissingProxyHosts(&errw, ws)
	want := "WARNING: /etc/hosts is missing proxy hostnames (api.app.jailoc.local, app.app.jailoc.local, app.jailoc.local); run 'jailoc proxy install' to update /etc/hosts\n"
	if got := errw.String(); got != want {
		t.Errorf("warning = %q, want %q", got, want)
	}

	if err := os.WriteFile(h.hostsPath, []byte(hostTestOriginal), 0o644); err != nil { //nolint:gosec // test hosts file
		t.Fatal(err)
	}
	if out, err := h.run("install", "--yes"); err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	errw.Reset()
	warnMissingProxyHosts(&errw, ws)
	if errw.Len() != 0 {
		t.Errorf("warning after install: %q", errw.String())
	}

	// An unreadable hosts file and a workspace without routes warn about nothing.
	currentProxyHostDeps.hostsPath = filepath.Join(h.home, "missing-hosts")
	warnMissingProxyHosts(&errw, ws)
	if errw.Len() != 0 {
		t.Errorf("warning for an unreadable hosts file: %q", errw.String())
	}
}

func TestLoadProxySavedOptionsUsesInstalledBroker(t *testing.T) {
	t.Setenv("HOME", shortTempDir(t, "po"))
	useProxyBrokerHost(t, proxyBrokerTarget{uid: testUID(), socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
	if got := loadProxySavedOptions(); got.listenerBroker || got.listen != proxyDefaultListen {
		t.Errorf("without options.json or broker = %+v, want the default listen", got)
	}

	useProxyBrokerHost(t, startTestListenerBroker(t))
	if got := loadProxySavedOptions(); !got.listenerBroker || got.listen != "" {
		t.Errorf("without options.json and a running broker = %+v, want listenerBroker", got)
	}

	if err := writeProxySavedOptions(proxyOptions{listen: "127.0.0.1:9000"}); err != nil {
		t.Fatal(err)
	}
	if got := loadProxySavedOptions(); got.listenerBroker || got.listen != "127.0.0.1:9000" {
		t.Errorf("saved options must win over the broker probe: %+v", got)
	}
}

func TestProxyScriptsAreConstant(t *testing.T) {
	t.Parallel()
	store := &proxyhost.LinuxCAStore{CertPath: "/x/ca.crt", UpdateCmd: []string{"update-ca-certificates"}, RemoveCmd: []string{"update-ca-certificates", "--fresh"}}
	for _, goos := range []string{"darwin", "linux"} {
		for _, withHosts := range []bool{false, true} {
			for _, s := range []*proxyhost.LinuxCAStore{nil, store} {
				scripts := map[string]string{
					"install":   proxyInstallScript(goos, withHosts, s),
					"uninstall": proxyUninstallScript(goos, withHosts, s),
				}
				for name, script := range scripts {
					if strings.Contains(script, "/x/ca.crt") {
						t.Errorf("%s/%s: store path interpolated into the script", goos, name)
					}
					if strings.Contains(script, "write_hosts \"$") != withHosts {
						t.Errorf("%s/%s: write_hosts presence != %v", goos, name, withHosts)
					}
				}
			}
		}
	}
	if got := fmt.Sprint(shellJoin([]string{"a b", "it's"})); got != `'a b' 'it'\''s'` {
		t.Errorf("shellJoin = %s", got)
	}
}

func mustResolveHostTestWorkspace(t *testing.T, name string) *workspace.Resolved {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := workspace.Resolve(cfg, name)
	if err != nil {
		t.Fatal(err)
	}
	return ws
}
