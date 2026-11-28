//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/brokerbin"
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

// caImport is a certificate file a trust tool imported.
type caImport struct {
	path string
	data []byte
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
	// broker is the listener broker binary the install gets.
	broker []byte
	// imports are the files the trust tools were told to import, read when
	// they ran.
	imports []caImport
	// files is what the privileged scripts installed, by destination.
	files map[string][]byte
	// proxy is the background proxy runningProxy reports.
	proxy     *proxyState
	restarted [][]string
	// restartBanner is what the restarted proxy prints to the writer it gets.
	restartBanner string
	// readErrs makes readFile fail for a path, like a file the user cannot
	// read.
	readErrs   map[string]error
	destChecks []string

	privErr error
	// privErrApplied makes a failing privileged call do its work first, like
	// a script that fails halfway.
	privErrApplied bool
	// beforePriv runs when the privileged call starts, like something
	// changing the host while the user confirms.
	beforePriv   func()
	destErr      error
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

	h := &hostHarness{t: t, home: home, hostsPath: hostsPath, goos: goos, stdinIsTTY: true, certutil: true, systemStore: true, files: map[string][]byte{}, broker: []byte(hostTestBroker)}
	old := currentProxyHostDeps
	currentProxyHostDeps = proxyHostDeps{
		goos:         goos,
		uid:          func() uint32 { return hostTestUID },
		homeDir:      func() (string, error) { return home, nil },
		hostsPath:    hostsPath,
		brokerBinary: func() ([]byte, error) { return h.broker, nil },
		dirExists: func(p string) bool {
			if p == "/usr/local/share/ca-certificates" {
				return h.systemStore
			}
			fi, err := os.Stat(p)
			return err == nil && fi.IsDir() && strings.HasPrefix(p, home)
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
			if h.recordCAImport(call) {
				// The tests assert on the CA path; the staged file the tool
				// really got is in h.imports.
				call[len(call)-1] = proxyCACertPath()
			}
			h.commands = append(h.commands, call)
			if h.commandErr != nil {
				return h.commandErr(call)
			}
			return nil
		},
		probeBroker: func(context.Context) error { return h.brokerAnswer },
		readFile: func(p string) ([]byte, error) {
			if err, ok := h.readErrs[p]; ok {
				return nil, err
			}
			if data, ok := h.files[p]; ok {
				return data, nil
			}
			return nil, os.ErrNotExist
		},
		checkDestDir: func(p string) error {
			h.destChecks = append(h.destChecks, p)
			return h.destErr
		},
		runningProxy: func() *proxyState { return h.proxy },
		restartProxy: func(_ context.Context, names []string, out io.Writer) error {
			h.restarted = append(h.restarted, names)
			_, _ = io.WriteString(out, h.restartBanner)
			return nil
		},
		interactive: func() bool { return h.stdinIsTTY },
		prompt: func(yes bool) *promptIO {
			return &promptIO{in: strings.NewReader(h.answers), out: io.Discard, accessible: true, yes: yes}
		},
	}
	t.Cleanup(func() { currentProxyHostDeps = old })
	return h
}

// recordCAImport reports whether call imports a CA file (security
// add-trusted-cert, certutil -A), the last argument, and records the file.
func (h *hostHarness) recordCAImport(call []string) bool {
	isImport := (call[0] == "security" && len(call) > 1 && call[1] == "add-trusted-cert") ||
		(call[0] == "certutil" && slices.Contains(call, "-A"))
	if !isImport {
		return false
	}
	path := call[len(call)-1]
	data, err := os.ReadFile(path) //nolint:gosec // G304: staged file in the jailoc temporary directory
	if err != nil {
		h.t.Errorf("%s imports %s, which cannot be read: %v", call[0], path, err)
		return true
	}
	h.imports = append(h.imports, caImport{path: path, data: data})
	return true
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
		data, err := os.ReadFile(v) //nolint:gosec // G304: staged file in the jailoc temporary directory
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
	if h.beforePriv != nil {
		h.beforePriv()
	}
	if h.privErr != nil && !h.privErrApplied {
		return h.privErr
	}
	if err := h.applyPrivileged(call); err != nil {
		return err
	}
	return h.privErr
}

// applyPrivileged does what the privileged script does to the files the
// tests look at: the hosts file, the service files and the system CA.
func (h *hostHarness) applyPrivileged(call privCall) error {
	if prev, ok := call.env["JAILOC_UNIT_PREV_SHA256"]; ok {
		if err := h.checkUnitPrev(call.env["JAILOC_UNIT_DST"], prev); err != nil {
			return err
		}
	}
	if src, ok := call.files["JAILOC_HOSTS_SRC"]; ok {
		if dst := call.env["JAILOC_HOSTS_DST"]; dst != h.hostsPath {
			h.t.Errorf("JAILOC_HOSTS_DST = %q, want %q", dst, h.hostsPath)
		}
		if prev := call.env["JAILOC_HOSTS_PREV_SHA256"]; prev != "" {
			cur, err := os.ReadFile(h.hostsPath)
			switch {
			case prev == "missing" && err == nil:
				return errors.New("hosts file was created while waiting for confirmation")
			case prev != "missing" && (err != nil || sha256Hex(cur) != prev):
				return errors.New("hosts file changed while waiting for confirmation")
			}
		}
		if err := os.WriteFile(h.hostsPath, src, 0o644); err != nil { //nolint:gosec // test hosts file
			h.t.Fatal(err)
		}
	}
	install := func(dstKey, srcKey string) {
		if src, ok := call.files[srcKey]; ok {
			h.files[call.env[dstKey]] = src
		}
	}
	if strings.Contains(call.script, `install_file 0755 "$JAILOC_HELPER_SRC"`) {
		install("JAILOC_HELPER_DST", "JAILOC_HELPER_SRC")
		install("JAILOC_UNIT_DST", "JAILOC_UNIT_SRC")
	}
	if _, ok := call.files["JAILOC_CA_SRC"]; ok {
		if err := h.checkSystemCA(call.env["JAILOC_CA_DST"], call.env["JAILOC_CA_PREV_SHA256"]); err != nil {
			return err
		}
	}
	install("JAILOC_CA_DST", "JAILOC_CA_SRC")
	if strings.Contains(call.script, `remove_ca_file "$JAILOC_CA_DST"`) {
		if err := h.checkSystemCA(call.env["JAILOC_CA_DST"], call.env["JAILOC_CA_EXPECTED_SHA256"]); err != nil {
			return err
		}
		delete(h.files, call.env["JAILOC_CA_DST"])
	}
	if strings.Contains(call.script, `rm -f "$JAILOC_HELPER_DST"`) {
		delete(h.files, call.env["JAILOC_HELPER_DST"])
		delete(h.files, call.env["JAILOC_UNIT_DST"])
	}
	return nil
}

// checkUnitPrev is what check_prev_file verifies before the service is
// touched: want is the SHA-256 the unit at dst must still have, or "missing".
func (h *hostHarness) checkUnitPrev(dst, want string) error {
	cur, ok := h.files[dst]
	switch {
	case want == "missing" && ok:
		return fmt.Errorf("%s appeared while waiting for confirmation", dst)
	case want != "missing" && (!ok || sha256Hex(cur) != want):
		return fmt.Errorf("%s changed while waiting for confirmation", dst)
	}
	return nil
}

// checkSystemCA is what the privileged script verifies before it replaces or
// removes the system CA at dst: want is the SHA-256 it must still have, or
// "missing" for no file. An empty want is not checked.
func (h *hostHarness) checkSystemCA(dst, want string) error {
	cur, ok := h.files[dst]
	switch {
	case want == "":
	case want == "missing" && ok:
		return fmt.Errorf("%s appeared while waiting for confirmation", dst)
	case want != "missing" && ok && sha256Hex(cur) != want:
		return fmt.Errorf("%s is not the certificate jailoc expected", dst)
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
	caPEM, err := os.ReadFile(caPath) //nolint:gosec // G304: test path in t.TempDir()
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
	if len(h.priv) != 0 {
		t.Errorf("an unchanged install ran %d privileged call(s); the running service and /etc/hosts stay untouched", len(h.priv))
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
		currentProxyHostDeps.brokerBinary = func() ([]byte, error) { return nil, errors.New("no broker in this build") }
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
	for _, want := range []string{`systemctl disable --now "$JAILOC_SERVICE_NAME"`, "systemctl daemon-reload", `remove_ca_file "$JAILOC_CA_DST" "$JAILOC_CA_EXPECTED_SHA256"`, "'update-ca-certificates' '--fresh'"} {
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
	assertContains(t, out, "Proxy host setup is not installed.")
	if len(h.priv) != 0 || len(h.commands) != 0 {
		t.Errorf("nothing is installed, yet %d privileged call(s) and commands %v ran", len(h.priv), h.commands)
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
		"(recorded by `jailoc proxy install`, not verified: login keychain)",
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

func TestLoadProxySavedOptionsKeepsInstalledServiceWhenBrokerDown(t *testing.T) {
	for _, tt := range []struct {
		name  string
		state proxyhost.InstallState
		saved *proxyOptions
		want  proxyOptions
	}{
		{"recorded service, broker down", proxyhost.InstallState{HelperPath: "/h", ServiceUnitPath: "/u"}, nil, proxyOptions{listenerBroker: true}},
		{"recorded helper only, broker down", proxyhost.InstallState{HelperPath: "/h"}, nil, proxyOptions{listenerBroker: true}},
		{"install state without a service, broker down", proxyhost.InstallState{HostsLedger: []byte("x")}, nil, proxyOptions{listen: proxyDefaultListen}},
		{"recorded service, explicit saved listen", proxyhost.InstallState{HelperPath: "/h", ServiceUnitPath: "/u"}, &proxyOptions{listen: "127.0.0.1:9000"}, proxyOptions{listen: "127.0.0.1:9000"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", shortTempDir(t, "po"))
			useProxyBrokerHost(t, proxyBrokerTarget{uid: testUID(), socketPath: "/nonexistent/b.sock", addr: proxyBrokerAddr})
			if err := proxyhost.WriteInstallState(proxyInstallStatePath(), tt.state); err != nil {
				t.Fatal(err)
			}
			if tt.saved != nil {
				if err := writeProxySavedOptions(*tt.saved); err != nil {
					t.Fatal(err)
				}
			}
			if got := loadProxySavedOptions(); got.listenerBroker != tt.want.listenerBroker || got.listen != tt.want.listen {
				t.Errorf("loadProxySavedOptions() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestProxyScriptsAreConstant(t *testing.T) {
	t.Parallel()
	store := &proxyhost.LinuxCAStore{CertPath: "/x/ca.crt", UpdateCmd: []string{"update-ca-certificates"}, RemoveCmd: []string{"update-ca-certificates", "--fresh"}}
	for _, goos := range []string{"darwin", "linux"} {
		for _, withService := range []bool{false, true} {
			for _, withHosts := range []bool{false, true} {
				for _, s := range []*proxyhost.LinuxCAStore{nil, store} {
					scripts := map[string]string{
						"install":   proxyInstallScript(goos, withService, withHosts, s),
						"uninstall": proxyUninstallScript(goos, withService, withHosts, s),
					}
					for name, script := range scripts {
						if strings.Contains(script, "/x/ca.crt") {
							t.Errorf("%s/%s: store path interpolated into the script", goos, name)
						}
						if strings.Contains(script, "write_hosts \"$") != withHosts {
							t.Errorf("%s/%s: write_hosts presence != %v", goos, name, withHosts)
						}
						hasService := strings.Contains(script, "launchctl") || strings.Contains(script, "systemctl")
						if hasService != withService {
							t.Errorf("%s/%s: service commands present = %v, want %v:\n%s", goos, name, hasService, withService, script)
						}
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

// Review hardening of the host setup.

// testCertPEM returns a self-signed certificate PEM and its SHA-256.
func testCertPEM(t *testing.T, cn string) (certPEM []byte, sum string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), sha256Hex(der)
}

// useNSSInspection answers `certutil -L -n "jailoc proxy CA" -a` with the PEM
// in entries for the database, or with certutil's "not found" failure.
func (h *hostHarness) useNSSInspection(entries map[string][]byte) {
	h.t.Helper()
	currentProxyHostDeps.runCommandOutput = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		i := slices.Index(args, "-d")
		if i < 0 || i+1 >= len(args) {
			h.t.Errorf("certutil without -d: %v", args)
			return nil, errors.New("bad call")
		}
		if data, ok := entries[strings.TrimPrefix(args[i+1], "sql:")]; ok {
			return data, nil
		}
		return []byte("certutil: Could not find cert: jailoc proxy CA\n"), errors.New("exit status 255")
	}
}

func (h *hostHarness) rotateCA() {
	h.t.Helper()
	for _, f := range []string{"ca.crt", "intermediate.crt", "intermediate.key", "cert.crt", "cert.key"} {
		if err := os.Remove(filepath.Join(proxyCADir(), f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			h.t.Fatal(err)
		}
	}
}

func (h *hostHarness) mustRun(args ...string) string {
	h.t.Helper()
	out, err := h.run(args...)
	if err != nil {
		h.t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

func runHelperScript(t *testing.T, body string, env map[string]string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-e", "-c", proxyPrivilegedHelpers+body) //nolint:gosec // G204: constant helper script under test
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestProxyInstallChecksDestinationDirectories(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	want := []string{"/usr/local/libexec/jailoc-listener-broker", "/etc/systemd/system/jailoc-listener-broker.service"}
	if !slices.Equal(h.destChecks, want) {
		t.Errorf("checked destinations = %v, want %v", h.destChecks, want)
	}

	h2 := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h2.destErr = errors.New("/usr/local is group writable")
	out, err := h2.run("install", "--yes")
	if err == nil || !strings.Contains(err.Error(), "/usr/local is group writable") {
		t.Fatalf("error = %v\n%s", err, out)
	}
	if len(h2.priv) != 0 || h2.hosts() != hostTestOriginal {
		t.Errorf("an untrusted destination reached the privileged step: %d call(s)", len(h2.priv))
	}
}

func TestProxyPrivilegedHelpersCheckInstallDirectories(t *testing.T) {
	base, err := filepath.EvalSymlinks(shortTempDir(t, "rc"))
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil { //nolint:gosec // G301: directory under test
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	writable := filepath.Join(base, "writable")
	if err := os.Mkdir(writable, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(writable, 0o775); err != nil { //nolint:gosec // G302: the mode under test
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, dir, want string
	}{
		{"symlink", link, "is a symlink; refusing to install into an untrusted directory"},
		{"group writable", writable, "is writable by group or others; refusing to install"},
	} {
		out, err := runHelperScript(t, `check_root_dir_chain "$JAILOC_TEST_DIR"`, map[string]string{"JAILOC_TEST_DIR": tc.dir})
		if err == nil || !strings.Contains(out, tc.want) {
			t.Errorf("%s: err = %v, output %q, want %q", tc.name, err, out, tc.want)
		}
	}
	if os.Geteuid() != 0 {
		out, err := runHelperScript(t, `check_root_dir_chain "$JAILOC_TEST_DIR"`, map[string]string{"JAILOC_TEST_DIR": real})
		if err == nil || !strings.Contains(out, "is not a root-owned directory; refusing to install") {
			t.Errorf("a directory chain not owned by root: err = %v, output %q", err, out)
		}
	}
	if !strings.Contains(proxyInstallScript("linux", true, false, nil), "install_file 0755") ||
		strings.Count(proxyPrivilegedHelpers, `check_root_dir_chain "$(dirname "$3")"`) != 2 {
		t.Error("install_file must check the directory chain before and after creating the directory")
	}
}

func TestServiceOwnedByUID(t *testing.T) {
	t.Parallel()
	const (
		linuxHelper  = "/usr/local/libexec/jailoc-listener-broker"
		darwinHelper = "/Library/PrivilegedHelperTools/jailoc-listener-broker"
	)
	unitFor := func(t *testing.T, goos string, uid uint32) []byte {
		t.Helper()
		var data []byte
		var err error
		if goos == "darwin" {
			data, err = proxyhost.RenderLaunchDaemonPlist(darwinHelper, uid)
		} else {
			data, err = proxyhost.RenderSystemdUnit(linuxHelper, uid)
		}
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, goos := range []string{"darwin", "linux"} {
		helper := linuxHelper
		if goos == "darwin" {
			helper = darwinHelper
		}
		for _, tc := range []struct {
			name        string
			onDisk      func(t *testing.T) []byte // nil: not installed
			wantPresent bool
			wantOwned   bool
		}{
			{"not installed", nil, false, false},
			{"identical unit", func(t *testing.T) []byte { return unitFor(t, goos, 501) }, true, true},
			{"unit with extra whitespace", func(t *testing.T) []byte { return append([]byte("\n"), append(unitFor(t, goos, 501), '\n', '\n')...) }, true, true},
			{"other user", func(t *testing.T) []byte { return unitFor(t, goos, 502) }, true, false},
			{"uid that starts with ours", func(t *testing.T) []byte { return unitFor(t, goos, 5010) }, true, false},
			{"uid that ends with ours", func(t *testing.T) []byte { return unitFor(t, goos, 1501) }, true, false},
			{"edited but still ours", func(t *testing.T) []byte {
				return bytes.Replace(unitFor(t, goos, 501), []byte("jailoc"), []byte("my jailoc"), 1)
			}, true, true},
		} {
			t.Run(goos+"/"+tc.name, func(t *testing.T) {
				t.Parallel()
				d := proxyHostDeps{goos: goos, readFile: func(string) ([]byte, error) {
					if tc.onDisk == nil {
						return nil, os.ErrNotExist
					}
					return tc.onDisk(t), nil
				}}
				present, owned, prev, err := serviceOwnedByUID(d, proxyhost.ServiceUnitPath(goos), helper, 501)
				if err != nil {
					t.Fatalf("serviceOwnedByUID() error = %v", err)
				}
				if present != tc.wantPresent || owned != tc.wantOwned {
					t.Errorf("serviceOwnedByUID() = (%v, %v), want (%v, %v)", present, owned, tc.wantPresent, tc.wantOwned)
				}
				wantPrev := "missing"
				if tc.onDisk != nil {
					wantPrev = sha256Hex(tc.onDisk(t))
				}
				if prev != wantPrev {
					t.Errorf("prevSHA256 = %q, want %q", prev, wantPrev)
				}
			})
		}
	}
}

func TestProxyUninstallLeavesServiceOfAnotherUser(t *testing.T) {
	foreignUnit := func(t *testing.T) []byte {
		t.Helper()
		unit, err := proxyhost.RenderSystemdUnit("/usr/local/libexec/jailoc-listener-broker", 777)
		if err != nil {
			t.Fatal(err)
		}
		return unit
	}

	t.Run("nothing of ours is installed", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.files[proxyhost.ServiceUnitPath("linux")] = foreignUnit(t)
		_, err := h.run("uninstall", "--yes")
		if err == nil || !strings.Contains(err.Error(), "belongs to another user; refusing to uninstall it") {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 {
			t.Error("the service of another user reached the privileged step")
		}
	})

	t.Run("our other changes are reverted", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.files[proxyhost.ServiceUnitPath("linux")] = foreignUnit(t)
		h.priv = nil

		out := h.mustRun("uninstall", "--yes")
		if len(h.priv) != 1 {
			t.Fatalf("privileged calls = %d, want 1", len(h.priv))
		}
		call := h.priv[0]
		for _, bad := range []string{"systemctl", `rm -f "$JAILOC_HELPER_DST"`, `rm -f "$JAILOC_UNIT_DST"`, "JAILOC_SOCKET_DIR"} {
			if strings.Contains(call.script, bad) {
				t.Errorf("the service of another user is touched by %q:\n%s", bad, call.script)
			}
		}
		for _, k := range []string{"JAILOC_HELPER_DST", "JAILOC_UNIT_DST", "JAILOC_SOCKET_DIR"} {
			if _, ok := call.env[k]; ok {
				t.Errorf("env %s is set for a service that stays", k)
			}
		}
		if !strings.Contains(call.script, `write_hosts "$`) || h.hosts() != hostTestOriginal {
			t.Errorf("the hosts block was not reverted:\n%s", h.hosts())
		}
		if _, ok := h.files[proxyhost.HelperBinaryPath("linux")]; !ok {
			t.Error("the helper of the other user was removed")
		}
		assertContains(t, out, "belongs to another user and was left in place")
		if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install.json still exists (%v)", err)
		}
	})

	t.Run("no privileged step needed", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		if err := os.WriteFile(h.hostsPath, []byte(hostTestOriginal), 0o644); err != nil { //nolint:gosec // test hosts file
			t.Fatal(err)
		}
		unit, err := proxyhost.RenderLaunchDaemonPlist(proxyhost.HelperBinaryPath("darwin"), 777)
		if err != nil {
			t.Fatal(err)
		}
		h.files[proxyhost.ServiceUnitPath("darwin")] = unit
		h.priv, h.commands = nil, nil

		h.mustRun("uninstall", "--yes")
		if len(h.priv) != 0 {
			t.Errorf("sudo ran %d time(s) with nothing to do as root", len(h.priv))
		}
		if !h.hasCommand("security", "delete-certificate") {
			t.Errorf("the keychain trust was not removed: %v", h.commands)
		}
	})
}

func TestProxyHostSetupRunsUnderTheCALock(t *testing.T) {
	for _, verb := range []string{"install", "uninstall"} {
		t.Run(verb, func(t *testing.T) {
			h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
			if verb == "uninstall" {
				h.mustRun("install", "--yes")
				h.priv = nil
			}
			var lockErr error
			h.beforePriv = func() {
				f, err := os.OpenFile(filepath.Join(proxyCADir(), ".lock"), os.O_RDWR, 0o600) //nolint:gosec // G304: test lock path
				if err != nil {
					lockErr = err
					return
				}
				defer func() { _ = f.Close() }()
				lockErr = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) //nolint:gosec // G115: file descriptors fit in int
			}
			h.mustRun(verb, "--yes")
			if len(h.priv) == 0 {
				t.Fatal("no privileged call")
			}
			if !errors.Is(lockErr, syscall.EWOULDBLOCK) {
				t.Errorf("another run could take the lock during the privileged step: %v", lockErr)
			}
		})
	}
}

func TestProxyInstallRejectsStaleHostsSnapshot(t *testing.T) {
	t.Run("hosts file changed while waiting", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		changed := hostTestOriginal + "10.0.0.1 corp.example\n"
		h.beforePriv = func() {
			if err := os.WriteFile(h.hostsPath, []byte(changed), 0o644); err != nil { //nolint:gosec // test hosts file
				t.Fatal(err)
			}
		}
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "changed while waiting for confirmation") {
			t.Fatalf("error = %v", err)
		}
		if h.hosts() != changed {
			t.Errorf("a stale snapshot overwrote the hosts file:\n%s", h.hosts())
		}
		if got := h.priv[0].env["JAILOC_HOSTS_PREV_SHA256"]; got != sha256Hex([]byte(hostTestOriginal)) {
			t.Errorf("JAILOC_HOSTS_PREV_SHA256 = %q, want the hash of the file jailoc planned against", got)
		}
	})
	t.Run("hosts file missing at planning time", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		if err := os.Remove(h.hostsPath); err != nil {
			t.Fatal(err)
		}
		h.mustRun("install", "--yes")
		if got := h.priv[0].env["JAILOC_HOSTS_PREV_SHA256"]; got != "missing" {
			t.Errorf("JAILOC_HOSTS_PREV_SHA256 = %q, want missing", got)
		}
	})
	t.Run("uninstall", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.priv = nil
		h.mustRun("uninstall", "--yes")
		if got := h.priv[0].env["JAILOC_HOSTS_PREV_SHA256"]; got == "" || got == "missing" {
			t.Errorf("JAILOC_HOSTS_PREV_SHA256 = %q, want the hash of the hosts file with the block", got)
		}
	})
}

func TestProxyPrivilegedWriteHostsChecksPreviousContent(t *testing.T) {
	const old, replacement = "127.0.0.1 localhost\n", "127.0.0.1 localhost\n127.0.0.1 app.jailoc.local\n"
	for _, tc := range []struct {
		name    string
		exists  bool
		prev    func() string
		wantErr string
	}{
		{"unchanged", true, func() string { return sha256Hex([]byte(old)) }, ""},
		{"changed", true, func() string { return sha256Hex([]byte("other\n")) }, "changed while waiting for confirmation"},
		{"created meanwhile", true, func() string { return "missing" }, "was created while waiting for confirmation"},
		{"still missing", false, func() string { return "missing" }, ""},
		{"removed meanwhile", false, func() string { return sha256Hex([]byte(old)) }, "changed while waiting for confirmation"},
		{"no snapshot", true, func() string { return "" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := shortTempDir(t, "wh")
			src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "hosts")
			if err := os.WriteFile(src, []byte(replacement), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.exists {
				if err := os.WriteFile(dst, []byte(old), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := runHelperScript(t, `write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"`, map[string]string{
				"JAILOC_HOSTS_SRC":         src,
				"JAILOC_HOSTS_SHA256":      sha256Hex([]byte(replacement)),
				"JAILOC_HOSTS_DST":         dst,
				"JAILOC_HOSTS_PREV_SHA256": tc.prev(),
			})
			got, _ := os.ReadFile(dst) //nolint:gosec // G304: test path in a temporary directory
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(out, tc.wantErr) {
					t.Fatalf("err = %v, output %q, want %q", err, out, tc.wantErr)
				}
				if tc.exists && string(got) != old {
					t.Errorf("hosts file = %q, want it untouched", got)
				}
				return
			}
			if err != nil || string(got) != replacement {
				t.Errorf("err = %v, output %q, hosts file = %q, want the replacement", err, out, got)
			}
		})
	}
}

func TestProxyInstallLeavesRunningServiceAlone(t *testing.T) {
	t.Run("only hosts change", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		if err := os.WriteFile(config.ConfigPath(), []byte(hostTestConfig+"\n[workspaces.desk.proxy]\nweb = 6080\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.priv, h.destChecks = nil, nil

		h.mustRun("install", "--yes")
		if len(h.priv) != 1 {
			t.Fatalf("privileged calls = %d, want 1", len(h.priv))
		}
		call := h.priv[0]
		for _, bad := range []string{"launchctl", "install_file 0"} {
			if strings.Contains(call.script, bad) {
				t.Errorf("a repeated install touches the running service with %q:\n%s", bad, call.script)
			}
		}
		if !strings.Contains(call.script, `write_hosts "$`) {
			t.Errorf("hosts were not updated:\n%s", call.script)
		}
		if _, ok := call.env["JAILOC_HELPER_SRC"]; ok || len(h.destChecks) != 0 {
			t.Errorf("the unchanged helper was staged (%v) or its directory checked (%v)", call.env, h.destChecks)
		}
		if st := h.installState(); st.HelperPath == "" || st.ServiceUnitPath == "" {
			t.Errorf("state lost the service: %+v", st)
		}
	})

	t.Run("broker not running", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.priv = nil
		h.brokerAnswer = errors.New("connection refused")
		h.beforePriv = func() { h.brokerAnswer = nil }
		h.mustRun("install", "--yes")
		if len(h.priv) != 1 || !strings.Contains(h.priv[0].script, "launchctl kickstart -k") {
			t.Errorf("a dead broker must be started again: %+v", h.priv)
		}
	})

	t.Run("changed helper restarts the attached proxy", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.proxy = &proxyState{ListenerBroker: true, Workspaces: map[string][]string{"zeta": nil, "alpha": nil}}
		h.mustRun("install", "--yes")
		if len(h.restarted) != 0 {
			t.Errorf("an unchanged install restarted the proxy: %v", h.restarted)
		}

		h.broker = []byte(hostTestBroker + "# v2\n")
		h.priv = nil
		h.mustRun("install", "--yes")
		if len(h.priv) != 1 || !strings.Contains(h.priv[0].script, "launchctl kickstart -k") {
			t.Errorf("a new helper must be installed and the service restarted: %+v", h.priv)
		}
		if want := [][]string{{"alpha", "zeta"}}; !reflect.DeepEqual(h.restarted, want) {
			t.Errorf("restarted proxies = %v, want %v", h.restarted, want)
		}
	})

	t.Run("no proxy to restart", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.proxy = &proxyState{ListenerBroker: false, Workspaces: map[string][]string{"alpha": nil}}
		h.mustRun("install", "--yes")
		if len(h.restarted) != 0 {
			t.Errorf("a proxy that does not use the broker was restarted: %v", h.restarted)
		}
	})
}

func TestProxyInstallRecordsPartialPrivilegedWork(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.privErr, h.privErrApplied = errors.New("systemctl restart failed"), true
	_, err := h.run("install", "--yes")
	if err == nil || !strings.Contains(err.Error(), "systemctl restart failed") {
		t.Fatalf("error = %v", err)
	}
	st := h.installState()
	caPEM, err := os.ReadFile(proxyCACertPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.SystemCACertSHA256 != sha256Hex(caPEM) {
		t.Errorf("state records the system CA as %q, want the SHA-256 of the installed file", st.SystemCACertSHA256)
	}
	if len(st.HostsLedger) == 0 || st.HelperPath != proxyhost.HelperBinaryPath("linux") ||
		st.ServiceUnitPath != proxyhost.ServiceUnitPath("linux") || st.SystemCACertPath != "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt" ||
		!slices.Equal(st.SystemCARemoveCmd, []string{"update-ca-certificates", "--fresh"}) {
		t.Errorf("state = %+v, want everything the script finished", st)
	}

	// What it recorded is enough for uninstall to revert it.
	h.privErr = nil
	h.priv = nil
	h.mustRun("uninstall", "--yes")
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts not restored:\n%s", h.hosts())
	}
	if len(h.files) != 0 {
		t.Errorf("installed files left behind: %v", h.files)
	}
}

func TestProxyInstallRecordsOnlyFinishedPrivilegedWork(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.privErr, h.privErrApplied = errors.New("boom"), true
	h.beforePriv = func() {
		// The script died before it wrote anything.
		h.files = map[string][]byte{}
		h.privErrApplied = false
	}
	if _, err := h.run("install", "--yes"); err == nil {
		t.Fatal("want an error")
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json written although nothing was installed (%v)", err)
	}
}

func TestProxyInstallRotationKeepsOldCAUntilItIsRemoved(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	ffDB := h.addNSSDatabase("Library/Application Support/Firefox/Profiles/abc.default")
	h.mustRun("install", "--yes")
	first := h.installState()
	oldSHA1 := pemSHA1(first.CACertPEM)

	h.rotateCA()
	h.commandErr = func(args []string) error {
		if (args[0] == "security" && args[1] == "delete-certificate") || (args[0] == "certutil" && slices.Contains(args, "-A")) {
			return errors.New("keychain is locked")
		}
		return nil
	}
	h.mustRun("install", "--yes")
	second := h.installState()
	if !slices.Equal(second.PendingKeychainSHA1s, []string{oldSHA1}) {
		t.Errorf("pending keychain SHA-1s = %v, want [%s]", second.PendingKeychainSHA1s, oldSHA1)
	}
	if !slices.Equal(second.PendingNSSCertSHA256s, []string{first.CACertSHA256}) || len(second.NSSDatabases) != 0 ||
		!slices.Equal(second.PendingNSSDatabases, []string{ffDB}) {
		t.Errorf("state = %+v, want the old CA fingerprint and database kept pending for NSS and no database trusting the new CA", second)
	}

	// The next install retries both.
	h.commandErr = nil
	h.commands = nil
	h.mustRun("install", "--yes")
	if !h.hasCommand("security", "delete-certificate", "-Z", oldSHA1) {
		t.Errorf("the old CA was not deleted again: %v", h.commands)
	}
	third := h.installState()
	if len(third.PendingKeychainSHA1s) != 0 || len(third.PendingNSSCertSHA256s) != 0 || len(third.PendingNSSDatabases) != 0 ||
		!slices.Equal(third.NSSDatabases, []string{ffDB}) {
		t.Errorf("pending entries remain after cleanup or the database lost the new CA: %+v", third)
	}
}

func TestProxyInstallKeychainDeleteOfMissingCertIsDone(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	h.rotateCA()
	h.commandErr = func(args []string) error {
		if args[0] == "security" && args[1] == "delete-certificate" {
			return errors.New("security: exit status 44: The specified item could not be found in the keychain.")
		}
		return nil
	}
	out := h.mustRun("install", "--yes")
	if st := h.installState(); len(st.PendingKeychainSHA1s) != 0 {
		t.Errorf("a certificate that is gone stays pending: %v\n%s", st.PendingKeychainSHA1s, out)
	}
}

func TestProxyInstallVerifiesNSSCertificateOwnership(t *testing.T) {
	foreignPEM, _ := testCertPEM(t, "someone else's jailoc proxy CA")

	t.Run("foreign certificate is not replaced", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.useNSSInspection(map[string][]byte{db: foreignPEM})

		out := h.mustRun("install", "--yes")
		if h.hasCommand("certutil", "-d", "sql:"+db, "-D") || h.hasCommand("certutil", "-d", "sql:"+db, "-A") {
			t.Errorf("a certificate jailoc does not own was replaced: %v", h.commands)
		}
		assertContains(t, out, `refusing to replace existing certificate "jailoc proxy CA" in `+db+" because its fingerprint does not match jailoc's recorded CA")
		assertContains(t, out, "by hand")
		if st := h.installState(); len(st.NSSDatabases) != 0 {
			t.Errorf("state claims a database whose certificate is not ours: %v", st.NSSDatabases)
		}
	})

	t.Run("current certificate is kept", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.mustRun("install", "--yes")
		caPEM, err := os.ReadFile(proxyCACertPath())
		if err != nil {
			t.Fatal(err)
		}
		h.useNSSInspection(map[string][]byte{db: caPEM})
		h.commands = nil

		h.mustRun("install", "--yes")
		if h.hasCommand("certutil", "-d", "sql:"+db, "-D") || h.hasCommand("certutil", "-d", "sql:"+db, "-A") {
			t.Errorf("an up to date certificate was deleted and added again: %v", h.commands)
		}
		if !h.hasCommand("certutil", "-d", "sql:"+db, "-M", "-t", "C,,", "-n", "jailoc proxy CA") {
			t.Errorf("the trust flags of a matching certificate were not set: %v", h.commands)
		}
		if st := h.installState(); !slices.Equal(st.NSSDatabases, []string{db}) {
			t.Errorf("NSS databases = %v, want [%s]", st.NSSDatabases, db)
		}
	})

	t.Run("trust of a matching certificate cannot be set", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.mustRun("install", "--yes")
		caPEM, err := os.ReadFile(proxyCACertPath())
		if err != nil {
			t.Fatal(err)
		}
		h.useNSSInspection(map[string][]byte{db: caPEM})
		h.commandErr = func(args []string) error {
			if args[0] == "certutil" && slices.Contains(args, "-M") {
				return errors.New("certutil: function failed")
			}
			return nil
		}

		out := h.mustRun("install", "--yes")
		assertContains(t, out, "could not set the trust of the proxy CA in "+db)
		assertContains(t, out, "by hand")
	})

	t.Run("previous jailoc CA is replaced", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.mustRun("install", "--yes")
		oldPEM, err := os.ReadFile(proxyCACertPath())
		if err != nil {
			t.Fatal(err)
		}
		h.rotateCA()
		h.useNSSInspection(map[string][]byte{db: oldPEM})
		h.commands = nil

		h.mustRun("install", "--yes")
		del := h.commandIndex("certutil", "-d", "sql:"+db, "-D", "-n", "jailoc proxy CA")
		add := h.commandIndex("certutil", "-d", "sql:"+db, "-A")
		if del < 0 || add < 0 || del > add {
			t.Errorf("want the old CA deleted before the new one is added: %v", h.commands)
		}
	})

	t.Run("old CA left by an earlier rotation is replaced", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.mustRun("install", "--yes")
		st := h.installState()
		leftover, leftoverSHA := testCertPEM(t, "jailoc proxy CA before the last rotation")
		st.PendingNSSCertSHA256s = []string{leftoverSHA}
		if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
			t.Fatal(err)
		}
		h.useNSSInspection(map[string][]byte{db: leftover})
		h.commands = nil

		h.mustRun("install", "--yes")
		if !h.hasCommand("certutil", "-d", "sql:"+db, "-D") || !h.hasCommand("certutil", "-d", "sql:"+db, "-A") {
			t.Errorf("a recorded old CA must be replaced: %v", h.commands)
		}
	})

	t.Run("certutil cannot list the database", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		currentProxyHostDeps.runCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
			return []byte("certutil: function failed: SEC_ERROR_BAD_DATABASE"), errors.New("exit status 255")
		}
		out := h.mustRun("install", "--yes")
		if h.hasCommand("certutil", "-d", "sql:"+db) {
			t.Errorf("a database that could not be inspected was changed: %v", h.commands)
		}
		assertContains(t, out, "could not look at the certificates of "+db)
	})
}

func TestProxyUninstallVerifiesNSSCertificateOwnership(t *testing.T) {
	foreignPEM, _ := testCertPEM(t, "someone else's jailoc proxy CA")
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	foreignDB := h.addNSSDatabase(".pki/nssdb")
	ownDB := h.addNSSDatabase(".mozilla/firefox/abc.default")
	goneDB := h.addNSSDatabase(".mozilla/firefox/def.default")
	h.mustRun("install", "--yes")
	caPEM, err := os.ReadFile(proxyCACertPath())
	if err != nil {
		t.Fatal(err)
	}
	// A database added after install must not be touched.
	newDB := h.addNSSDatabase(".mozilla/firefox/new.default")
	h.useNSSInspection(map[string][]byte{foreignDB: foreignPEM, ownDB: caPEM, newDB: caPEM})
	h.commands = nil

	out := h.mustRun("uninstall", "--yes")
	if h.hasCommand("certutil", "-d", "sql:"+foreignDB, "-D") {
		t.Errorf("a certificate that is not ours was deleted: %v", h.commands)
	}
	assertContains(t, out, "not removing certificate \"jailoc proxy CA\" from "+foreignDB)
	if !h.hasCommand("certutil", "-d", "sql:"+ownDB, "-D", "-n", "jailoc proxy CA") {
		t.Errorf("our certificate was not deleted: %v", h.commands)
	}
	if h.hasCommand("certutil", "-d", "sql:"+goneDB, "-D") {
		t.Errorf("a certificate that is not there was deleted: %v", h.commands)
	}
	if h.hasCommand("certutil", "-d", "sql:"+newDB) {
		t.Errorf("a database that install did not record was touched: %v", h.commands)
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

func TestProxyUninstallKeepsStateWhenTrustRemovalFails(t *testing.T) {
	t.Run("keychain", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		before := h.installState()
		h.commandErr = func(args []string) error {
			if args[0] == "security" {
				return errors.New("user canceled")
			}
			return nil
		}
		h.priv = nil
		out, err := h.run("uninstall", "--yes")
		if err == nil || !strings.Contains(err.Error(), "user canceled") {
			t.Fatalf("error = %v\n%s", err, out)
		}
		st := h.installState()
		if !st.MacOSKeychainCert || st.CACertSHA256 != before.CACertSHA256 || !bytes.Equal(st.CACertPEM, before.CACertPEM) {
			t.Errorf("state = %+v, want the keychain trust kept for a retry", st)
		}
		if len(st.HostsLedger) != 0 || st.HelperPath != "" || st.ServiceUnitPath != "" || st.SystemCACertPath != "" {
			t.Errorf("state keeps what the privileged step removed: %+v", st)
		}
		if h.hosts() != hostTestOriginal || len(h.priv) != 1 {
			t.Errorf("privileged part: %d call(s), hosts:\n%s", len(h.priv), h.hosts())
		}

		// The retry finishes without sudo.
		h.commandErr = nil
		h.commands = nil
		h.mustRun("uninstall", "--yes")
		if len(h.priv) != 1 {
			t.Errorf("the retry ran %d privileged call(s) in total, want the 1 of the first run", len(h.priv))
		}
		if !h.hasCommand("security", "delete-certificate", "-Z", pemSHA1(before.CACertPEM)) {
			t.Errorf("the retry did not remove the keychain trust: %v", h.commands)
		}
		if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install.json still exists after the retry (%v)", err)
		}
	})

	t.Run("pending keychain certificates", func(t *testing.T) {
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		st := h.installState()
		st.PendingKeychainSHA1s = []string{"AA" + strings.Repeat("00", 19)}
		if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
			t.Fatal(err)
		}
		h.commandErr = func(args []string) error {
			if args[0] == "security" && slices.Contains(args, st.PendingKeychainSHA1s[0]) {
				return errors.New("keychain is locked")
			}
			return nil
		}
		if _, err := h.run("uninstall", "--yes"); err == nil {
			t.Fatal("want an error")
		}
		got := h.installState()
		if !slices.Equal(got.PendingKeychainSHA1s, st.PendingKeychainSHA1s) || got.MacOSKeychainCert {
			t.Errorf("state = %+v, want only the pending certificate kept", got)
		}
	})

	t.Run("NSS database", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		okDB := h.addNSSDatabase(".pki/nssdb")
		badDB := h.addNSSDatabase(".mozilla/firefox/abc.default")
		h.mustRun("install", "--yes")
		h.commandErr = func(args []string) error {
			if args[0] == "certutil" && slices.Contains(args, "-D") && slices.Contains(args, "sql:"+badDB) {
				return errors.New("database is locked")
			}
			return nil
		}
		if _, err := h.run("uninstall", "--yes"); err == nil || !strings.Contains(err.Error(), "database is locked") {
			t.Fatalf("error = %v", err)
		}
		st := h.installState()
		if !slices.Equal(st.NSSDatabases, []string{badDB}) || st.CACertSHA256 == "" {
			t.Errorf("state = %+v, want only %s kept", st, badDB)
		}
		_ = okDB
	})

	t.Run("certutil missing", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		db := h.addNSSDatabase(".pki/nssdb")
		h.mustRun("install", "--yes")
		h.certutil = false
		if _, err := h.run("uninstall", "--yes"); err == nil {
			t.Fatal("want an error")
		}
		if st := h.installState(); !slices.Equal(st.NSSDatabases, []string{db}) {
			t.Errorf("state = %+v, want %s kept", st, db)
		}
	})
}

func TestProxyUninstallKeepsProgressWhenPrivilegedStepFails(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	h.privErr, h.privErrApplied = errors.New("systemctl failed"), true
	h.priv = nil
	if _, err := h.run("uninstall", "--yes"); err == nil || !strings.Contains(err.Error(), "systemctl failed") {
		t.Fatalf("error = %v", err)
	}
	st := h.installState()
	if len(st.HostsLedger) != 0 || st.HelperPath != "" || st.ServiceUnitPath != "" {
		t.Errorf("state = %+v, want what the script finished dropped", st)
	}
	if st.SystemCACertPath == "" || st.CACertSHA256 == "" {
		t.Errorf("state = %+v, want the rest kept", st)
	}

	// The retry works with the hosts block and the service already gone.
	h.privErr = nil
	out := h.mustRun("uninstall", "--yes")
	assertContains(t, out, "Uninstalled the jailoc proxy host setup.")
	if h.hosts() != hostTestOriginal {
		t.Errorf("hosts = %q", h.hosts())
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

func TestProxyUninstallAfterHostsBlockWasRemoved(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	if err := os.WriteFile(h.hostsPath, []byte(hostTestOriginal), 0o644); err != nil { //nolint:gosec // test hosts file
		t.Fatal(err)
	}
	h.priv = nil
	out := h.mustRun("uninstall", "--yes")
	if strings.Contains(out, "Removed the proxy block") {
		t.Errorf("the block was removed before:\n%s", out)
	}
	if len(h.priv) != 1 || strings.Contains(h.priv[0].script, `write_hosts "$`) {
		t.Errorf("privileged calls = %+v, want only the service cleanup", h.priv)
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

func TestProxyStatusNamesTrustStores(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.addNSSDatabase(".pki/nssdb")
	h.mustRun("install", "--yes")
	out := h.mustRun("status")
	assertContains(t, out, "(verified: system store; recorded by `jailoc proxy install`, not verified: 1 NSS database(s))")

	st := h.installState()
	st.SystemCACertPath, st.NSSDatabases = "", nil
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	out = h.mustRun("status")
	assertContains(t, out, "not trusted in any store by `jailoc proxy install`; trust it in your browser or run `jailoc proxy install`")

	st.CACertSHA256 = ""
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	out = h.mustRun("status")
	assertContains(t, out, "not trusted in any store")

	h2 := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h2.mustRun("install", "--yes")
	assertContains(t, h2.mustRun("status"), "(recorded by `jailoc proxy install`, not verified: login keychain)")
}

func TestProxyInstallExplainsHowToReconcileHostsBlock(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal+"# BEGIN jailoc proxy hosts\n127.0.0.1 app.jailoc.local\n# END jailoc proxy hosts\n")
	_, err := h.run("install", "--yes")
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{
		"run `jailoc proxy uninstall`",
		"remove both the `# BEGIN/END jailoc proxy hosts` block in " + h.hostsPath + " and " + proxyInstallStatePath() + " to reconcile them",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if len(h.priv) != 0 {
		t.Error("an unreconciled hosts file reached the privileged step")
	}
}

const hostTestSystemCA = "/usr/local/share/ca-certificates/jailoc-proxy-ca.crt"

func TestProxyInstallRecordsSystemCAIdentity(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	caPEM, err := os.ReadFile(proxyCACertPath())
	if err != nil {
		t.Fatal(err)
	}
	if got := h.installState().SystemCACertSHA256; got != sha256Hex(caPEM) {
		t.Errorf("SystemCACertSHA256 = %q, want %q", got, sha256Hex(caPEM))
	}
	if got := h.priv[0].env["JAILOC_CA_PREV_SHA256"]; got != "missing" {
		t.Errorf("JAILOC_CA_PREV_SHA256 = %q, want missing for a first install", got)
	}
}

func TestProxyInstallVerifiesSystemCAOwnership(t *testing.T) {
	foreignPEM, _ := testCertPEM(t, "an administrator's CA")

	t.Run("foreign certificate is not overwritten", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.files[hostTestSystemCA] = foreignPEM

		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "not installed by this user; refusing to overwrite it") {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 || !bytes.Equal(h.files[hostTestSystemCA], foreignPEM) {
			t.Errorf("the foreign certificate reached the privileged step or was replaced (%d call(s))", len(h.priv))
		}
		if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install.json written for an install that was refused (%v)", err)
		}
	})

	t.Run("own certificate of an earlier CA is replaced", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		first := h.installState()
		h.rotateCA()
		h.priv = nil

		h.mustRun("install", "--yes")
		if got, want := h.priv[0].env["JAILOC_CA_PREV_SHA256"], sha256Hex(first.CACertPEM); got != want {
			t.Errorf("JAILOC_CA_PREV_SHA256 = %q, want the hash of the file on disk %q", got, want)
		}
		if bytes.Equal(h.files[hostTestSystemCA], first.CACertPEM) {
			t.Error("the system CA was not replaced after the rotation")
		}
	})

	t.Run("partial install is still ours", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.privErr, h.privErrApplied = errors.New("systemctl restart failed"), true
		if _, err := h.run("install", "--yes"); err == nil {
			t.Fatal("want an error")
		}
		st := h.installState()
		if len(st.CACertPEM) != 0 || st.SystemCACertSHA256 == "" {
			t.Fatalf("state = %+v, want the system CA identity without a CA PEM", st)
		}
		h.privErr = nil
		h.rotateCA()
		h.mustRun("install", "--yes")
		if got := h.priv[len(h.priv)-1].env["JAILOC_CA_PREV_SHA256"]; got != st.SystemCACertSHA256 {
			t.Errorf("JAILOC_CA_PREV_SHA256 = %q, want the recorded %q", got, st.SystemCACertSHA256)
		}
	})
}

func TestProxyUninstallVerifiesSystemCAOwnership(t *testing.T) {
	foreignPEM, _ := testCertPEM(t, "an administrator's CA")

	t.Run("own certificate is removed with its hash", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		caPEM := h.files[hostTestSystemCA]
		h.priv = nil

		h.mustRun("uninstall", "--yes")
		if got := h.priv[0].env["JAILOC_CA_EXPECTED_SHA256"]; got != sha256Hex(caPEM) {
			t.Errorf("JAILOC_CA_EXPECTED_SHA256 = %q, want %q", got, sha256Hex(caPEM))
		}
		if _, ok := h.files[hostTestSystemCA]; ok {
			t.Error("the system CA was not removed")
		}
	})

	t.Run("foreign certificate stays", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.files[hostTestSystemCA] = foreignPEM
		h.priv = nil

		out := h.mustRun("uninstall", "--yes")
		assertContains(t, out, "not removing "+hostTestSystemCA+" because its certificate does not match jailoc's recorded CA")
		if !bytes.Equal(h.files[hostTestSystemCA], foreignPEM) {
			t.Error("a certificate that is not ours was removed")
		}
		if len(h.priv) != 1 || strings.Contains(h.priv[0].script, "JAILOC_CA_DST") {
			t.Errorf("the foreign certificate reached the privileged script: %+v", h.priv)
		}
		if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("install.json still exists (%v)", err)
		}
	})

	t.Run("certificate without a recorded identity stays", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		st := h.installState()
		st.SystemCACertSHA256, st.CACertPEM = "", nil
		if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
			t.Fatal(err)
		}
		caPEM := h.files[hostTestSystemCA]

		h.mustRun("uninstall", "--yes")
		if !bytes.Equal(h.files[hostTestSystemCA], caPEM) {
			t.Error("a certificate whose owner cannot be shown was removed")
		}
	})

	t.Run("certificate that is gone still refreshes the system bundle", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		delete(h.files, hostTestSystemCA)
		h.priv = nil

		h.mustRun("uninstall", "--yes")
		if len(h.priv) != 1 || !strings.Contains(h.priv[0].script, "'update-ca-certificates' '--fresh'") {
			t.Errorf("privileged calls = %+v, want the bundle refreshed", h.priv)
		}
	})
}

func TestProxyPrivilegedSystemCAHelpersCheckOwnership(t *testing.T) {
	dir := shortTempDir(t, "ca")
	dst := filepath.Join(dir, "jailoc-proxy-ca.crt")
	src := filepath.Join(dir, "src.crt")
	ours, theirs, fresh := []byte("ours\n"), []byte("theirs\n"), []byte("fresh\n")
	if err := os.WriteFile(src, fresh, 0o600); err != nil {
		t.Fatal(err)
	}
	reset := func(t *testing.T, content []byte) {
		t.Helper()
		_ = os.Remove(dst)
		if content != nil {
			if err := os.WriteFile(dst, content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	env := func(extra map[string]string) map[string]string {
		e := map[string]string{"SRC": src, "DST": dst, "SHA": sha256Hex(fresh)}
		for k, v := range extra {
			e[k] = v
		}
		return e
	}
	// install_file is root-only: its directory checks and install(1) -o 0 are
	// replaced to run it here.
	const stubs = "check_root_dir_chain() { :; }\ninstall() { cp \"$7\" \"$8\"; }\n"

	for _, tc := range []struct {
		name    string
		onDisk  []byte
		prev    string
		wantErr bool
	}{
		{"first install", nil, "missing", false},
		{"file appeared", theirs, "missing", true},
		{"file is the recorded one", ours, sha256Hex(ours), false},
		{"file is another one", theirs, sha256Hex(ours), true},
		{"recorded file is gone", nil, sha256Hex(ours), true},
	} {
		t.Run("install/"+tc.name, func(t *testing.T) {
			reset(t, tc.onDisk)
			out, err := runHelperScript(t, stubs+`install_file 0644 "$SRC" "$DST" "$SHA" "$PREV"`, env(map[string]string{"PREV": tc.prev}))
			got, _ := os.ReadFile(dst) //nolint:gosec // G304: test path
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, output %q, want error: %v", err, out, tc.wantErr)
			}
			if tc.wantErr && !bytes.Equal(got, tc.onDisk) {
				t.Errorf("the file on disk was changed to %q", got)
			}
			if !tc.wantErr && !bytes.Equal(got, fresh) {
				t.Errorf("the file on disk is %q, want the new certificate", got)
			}
			if _, statErr := os.Stat(dst + ".jailoc-new"); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("a staged copy was left behind (%v)", statErr)
			}
		})
	}

	for _, tc := range []struct {
		name    string
		onDisk  []byte
		wantErr bool
	}{
		{"own certificate", ours, false},
		{"certificate that is gone", nil, false},
		{"foreign certificate", theirs, true},
	} {
		t.Run("remove/"+tc.name, func(t *testing.T) {
			reset(t, tc.onDisk)
			out, err := runHelperScript(t, `remove_ca_file "$DST" "$SHA"`, map[string]string{"DST": dst, "SHA": sha256Hex(ours)})
			_, statErr := os.Stat(dst)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, output %q, want error: %v", err, out, tc.wantErr)
			}
			if exists := statErr == nil; exists != tc.wantErr {
				t.Errorf("file exists = %v, want %v", exists, tc.wantErr)
			}
		})
	}

	t.Run("remove/symlink", func(t *testing.T) {
		reset(t, nil)
		if err := os.Symlink(src, dst); err != nil {
			t.Fatal(err)
		}
		if _, err := runHelperScript(t, `remove_ca_file "$DST" "$SHA"`, map[string]string{"DST": dst, "SHA": sha256Hex(fresh)}); err == nil {
			t.Error("a symlink at the system CA path was followed")
		}
		if _, err := os.Lstat(dst); err != nil {
			t.Errorf("the symlink was removed: %v", err)
		}
	})
}

func TestProxyInstallRefusesServiceOfAnotherUser(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	unit, err := proxyhost.RenderSystemdUnit(proxyhost.HelperBinaryPath("linux"), 777)
	if err != nil {
		t.Fatal(err)
	}
	h.files[proxyhost.ServiceUnitPath("linux")] = unit

	_, err = h.run("install", "--yes")
	if err == nil || !strings.Contains(err.Error(), "belongs to another user; refusing to replace it") {
		t.Fatalf("error = %v", err)
	}
	if len(h.priv) != 0 || !bytes.Equal(h.files[proxyhost.ServiceUnitPath("linux")], unit) {
		t.Errorf("the service of another user was replaced (%d privileged call(s))", len(h.priv))
	}
}

func TestProxyInstallKeepsSystemCARecordWithoutStore(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	first := h.installState()

	// The CA update tool is gone from PATH, nothing removed the certificate.
	h.systemStore = false
	h.mustRun("install", "--yes")
	second := h.installState()
	if second.SystemCACertPath != first.SystemCACertPath || second.SystemCACertSHA256 != first.SystemCACertSHA256 ||
		!slices.Equal(second.SystemCAUpdateCmd, first.SystemCAUpdateCmd) || !slices.Equal(second.SystemCARemoveCmd, first.SystemCARemoveCmd) {
		t.Fatalf("state = %+v, want the system CA of %+v kept", second, first)
	}

	h.mustRun("uninstall", "--yes")
	if _, ok := h.files[hostTestSystemCA]; ok {
		t.Error("uninstall did not remove the system CA that install recorded")
	}
}

func TestProxyInstallPersistsOldCAIdentitiesBeforeRotating(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	ffDB := h.addNSSDatabase("Library/Application Support/Firefox/Profiles/abc.default")
	h.mustRun("install", "--yes")
	first := h.installState()
	oldSHA1 := pemSHA1(first.CACertPEM)
	h.rotateCA()

	// Saving the proxy options fails after the first state write.
	if err := os.Remove(proxyOptionsFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(proxyOptionsFile(), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := h.run("install", "--yes"); err == nil || !strings.Contains(err.Error(), "save proxy options") {
		t.Fatalf("error = %v", err)
	}
	second := h.installState()
	if second.CACertSHA256 == first.CACertSHA256 {
		t.Fatal("the CA was not rotated")
	}
	if !slices.Equal(second.PendingKeychainSHA1s, []string{oldSHA1}) || !slices.Equal(second.PendingNSSCertSHA256s, []string{first.CACertSHA256}) ||
		len(second.NSSDatabases) != 0 || !slices.Equal(second.PendingNSSDatabases, []string{ffDB}) {
		t.Fatalf("state = %+v, want the old CA kept as pending", second)
	}

	// The next install retires them and clears the lists.
	if err := os.Remove(proxyOptionsFile()); err != nil {
		t.Fatal(err)
	}
	h.commands = nil
	h.mustRun("install", "--yes")
	if !h.hasCommand("security", "delete-certificate", "-Z", oldSHA1) {
		t.Errorf("the old CA was not deleted: %v", h.commands)
	}
	if third := h.installState(); len(third.PendingKeychainSHA1s) != 0 || len(third.PendingNSSCertSHA256s) != 0 || len(third.PendingNSSDatabases) != 0 {
		t.Errorf("pending entries remain after cleanup: %+v", third)
	}
}

func TestProxyInstallPrunesDeletedNSSDatabases(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	keepDB := h.addNSSDatabase(".pki/nssdb")
	goneDB := h.addNSSDatabase(".mozilla/firefox/abc.default")
	h.mustRun("install", "--yes")
	if err := os.RemoveAll(goneDB); err != nil {
		t.Fatal(err)
	}

	h.mustRun("install", "--yes")
	if st := h.installState(); !slices.Equal(st.NSSDatabases, []string{keepDB}) {
		t.Errorf("NSS databases = %v, want [%s]", st.NSSDatabases, keepDB)
	}
}

func TestProxyUninstallSkipsDeletedNSSDatabases(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	goneDB := h.addNSSDatabase(".mozilla/firefox/abc.default")
	h.mustRun("install", "--yes")
	if err := os.RemoveAll(goneDB); err != nil {
		t.Fatal(err)
	}
	// certutil cannot open a database that is not there.
	currentProxyHostDeps.runCommandOutput = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("certutil: function failed: SEC_ERROR_BAD_DATABASE"), errors.New("exit status 255")
	}
	h.commands = nil

	out := h.mustRun("uninstall", "--yes")
	if h.hasCommand("certutil", "-d", "sql:"+goneDB) {
		t.Errorf("a deleted profile was inspected: %v", h.commands)
	}
	assertContains(t, out, "Uninstalled the jailoc proxy host setup.")
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

// Host-wide lock, service revalidation and fail-closed service reads.

func TestProxyScriptsLockAndRevalidateBeforeMutating(t *testing.T) {
	t.Parallel()
	store := &proxyhost.LinuxCAStore{CertPath: "/x/ca.crt", UpdateCmd: []string{"update-ca-certificates"}, RemoveCmd: []string{"update-ca-certificates", "--fresh"}}
	for _, goos := range []string{"darwin", "linux"} {
		scripts := map[string]string{
			"install":   proxyInstallScript(goos, true, true, store),
			"uninstall": proxyUninstallScript(goos, true, true, store),
		}
		for name, full := range scripts {
			body := strings.TrimPrefix(full, proxyPrivilegedHelpers)
			lock := strings.Index(body, "acquire_host_lock\n")
			check := strings.Index(body, `check_prev_file "$JAILOC_UNIT_DST" "$JAILOC_UNIT_PREV_SHA256"`)
			if lock != 0 || check < 0 || check < lock {
				t.Errorf("%s/%s: lock at %d, unit check at %d; want the lock first and the unit checked next:\n%s", goos, name, lock, check, body)
			}
			for _, mutation := range []string{`install_file 0755`, `launchctl`, `systemctl`, `rm -f`, `write_hosts "$`, `remove_ca_file "$`} {
				if i := strings.Index(body, mutation); i >= 0 && i < check {
					t.Errorf("%s/%s: %q runs before the unit is revalidated", goos, name, mutation)
				}
			}
		}
		if !strings.Contains(scripts["install"], `"$JAILOC_UNIT_SHA256" "$JAILOC_UNIT_PREV_SHA256"`) {
			t.Errorf("%s: the unit is installed without its previous hash", goos)
		}
	}
	for name, script := range map[string]string{
		"install":   proxyInstallScript("linux", false, true, nil),
		"uninstall": proxyUninstallScript("linux", false, true, nil),
	} {
		if strings.Contains(script, "check_prev_file \"$") {
			t.Errorf("%s without a service checks the service unit", name)
		}
		if !strings.Contains(script, "\nacquire_host_lock\n") {
			t.Errorf("%s without a service does not take the host lock", name)
		}
	}
}

func hostLockDirs(t *testing.T) (parent, lock string) {
	t.Helper()
	parent = shortTempDir(t, "hl")
	if err := os.Chmod(parent, 0o755); err != nil { //nolint:gosec // G302: the mode the root-owned lock directory has
		t.Fatal(err)
	}
	return parent, filepath.Join(parent, ".lock")
}

func TestProxyPrivilegedAcquireHostLock(t *testing.T) {
	t.Run("holds the lock until the script exits", func(t *testing.T) {
		_, lock := hostLockDirs(t)
		out, err := runHelperScript(t, `acquire_host_lock
[ -d "$JAILOC_HOST_LOCK_DIR" ] || { echo "no lock held"; exit 1; }
echo done`, map[string]string{"JAILOC_HOST_LOCK_DIR": lock})
		if err != nil || !strings.Contains(out, "done") {
			t.Fatalf("err = %v, output %q", err, out)
		}
		if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the lock outlived the script (%v)", err)
		}
	})
	t.Run("releases the lock when the script fails", func(t *testing.T) {
		_, lock := hostLockDirs(t)
		_, err := runHelperScript(t, "acquire_host_lock\nfalse", map[string]string{"JAILOC_HOST_LOCK_DIR": lock})
		if err == nil {
			t.Fatal("want the failure of the script")
		}
		if _, err := os.Stat(lock); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the lock outlived the failed script (%v)", err)
		}
	})
	t.Run("fails closed when another run holds the lock", func(t *testing.T) {
		_, lock := hostLockDirs(t)
		if err := os.Mkdir(lock, 0o700); err != nil {
			t.Fatal(err)
		}
		out, err := runHelperScript(t, "acquire_host_lock\necho got it", map[string]string{"JAILOC_HOST_LOCK_DIR": lock, "JAILOC_HOST_LOCK_TRIES": "3"})
		if err == nil || !strings.Contains(out, "another jailoc proxy setup holds") || strings.Contains(out, "got it") {
			t.Fatalf("err = %v, output %q", err, out)
		}
		if _, err := os.Stat(lock); err != nil {
			t.Errorf("the lock of the other run was removed: %v", err)
		}
	})
	t.Run("refuses an untrusted lock parent", func(t *testing.T) {
		parent, lock := hostLockDirs(t)
		if err := os.Chmod(parent, 0o775); err != nil { //nolint:gosec // G302: the mode under test
			t.Fatal(err)
		}
		out, err := runHelperScript(t, "acquire_host_lock", map[string]string{"JAILOC_HOST_LOCK_DIR": lock})
		if err == nil || !strings.Contains(out, "is writable by group or others") {
			t.Errorf("group writable parent: err = %v, output %q", err, out)
		}

		real, _ := hostLockDirs(t)
		link := filepath.Join(shortTempDir(t, "hl"), "link")
		if err := os.Symlink(real, link); err != nil {
			t.Fatal(err)
		}
		out, err = runHelperScript(t, "acquire_host_lock", map[string]string{"JAILOC_HOST_LOCK_DIR": filepath.Join(link, ".lock")})
		if err == nil || !strings.Contains(out, "is a symlink") {
			t.Errorf("symlinked parent: err = %v, output %q", err, out)
		}
		if _, err := os.Stat(filepath.Join(real, ".lock")); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a lock was created below an untrusted parent (%v)", err)
		}
	})
}

func TestProxyPrivilegedCheckPrevFile(t *testing.T) {
	const content = "[Unit]\n"
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, dst string)
		prev    string
		wantErr string
	}{
		{"missing and absent", func(*testing.T, string) {}, "missing", ""},
		{"missing but present", func(t *testing.T, dst string) {
			if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "missing", "appeared while waiting for confirmation"},
		{"missing but dangling symlink", func(t *testing.T, dst string) {
			if err := os.Symlink(dst+".nowhere", dst); err != nil {
				t.Fatal(err)
			}
		}, "missing", "appeared while waiting for confirmation"},
		{"unchanged", func(t *testing.T, dst string) {
			if err := os.WriteFile(dst, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}, sha256Hex([]byte(content)), ""},
		{"changed", func(t *testing.T, dst string) {
			if err := os.WriteFile(dst, []byte("other\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, sha256Hex([]byte(content)), "changed while waiting for confirmation"},
		{"removed", func(*testing.T, string) {}, sha256Hex([]byte(content)), "changed while waiting for confirmation"},
		{"symlink to the planned content", func(t *testing.T, dst string) {
			if err := os.WriteFile(dst+".real", []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(dst+".real", dst); err != nil {
				t.Fatal(err)
			}
		}, sha256Hex([]byte(content)), "changed while waiting for confirmation"},
		{"directory", func(t *testing.T, dst string) {
			if err := os.Mkdir(dst, 0o700); err != nil {
				t.Fatal(err)
			}
		}, sha256Hex([]byte(content)), "changed while waiting for confirmation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(shortTempDir(t, "pf"), "unit")
			tc.setup(t, dst)
			out, err := runHelperScript(t, `check_prev_file "$JAILOC_TEST_DST" "$JAILOC_TEST_PREV"`, map[string]string{"JAILOC_TEST_DST": dst, "JAILOC_TEST_PREV": tc.prev})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("err = %v, output %q", err, out)
				}
				return
			}
			if err == nil || !strings.Contains(out, tc.wantErr) {
				t.Fatalf("err = %v, output %q, want %q", err, out, tc.wantErr)
			}
		})
	}
}

func TestProxyPrivilegedWriteHostsRefusesDanglingSymlinkWhenMissing(t *testing.T) {
	dir := shortTempDir(t, "wh")
	src, dst := filepath.Join(dir, "src"), filepath.Join(dir, "hosts")
	if err := os.WriteFile(src, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "nowhere"), dst); err != nil {
		t.Fatal(err)
	}
	out, err := runHelperScript(t, `write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"`, map[string]string{
		"JAILOC_HOSTS_SRC": src, "JAILOC_HOSTS_SHA256": sha256Hex([]byte("x\n")), "JAILOC_HOSTS_DST": dst, "JAILOC_HOSTS_PREV_SHA256": "missing",
	})
	if err == nil || !strings.Contains(out, "was created while waiting for confirmation") {
		t.Fatalf("err = %v, output %q", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "nowhere")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the dangling symlink was written through (%v)", err)
	}
}

func TestServiceOwnedByUIDFailsClosedOnReadErrors(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			t.Parallel()
			d := proxyHostDeps{goos: goos, readFile: func(string) ([]byte, error) { return nil, os.ErrPermission }}
			present, owned, prev, err := serviceOwnedByUID(d, proxyhost.ServiceUnitPath(goos), proxyhost.HelperBinaryPath(goos), 501)
			if err == nil || !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), proxyhost.ServiceUnitPath(goos)) {
				t.Fatalf("error = %v, want a wrapped permission error naming the unit", err)
			}
			if present || owned || prev != "" {
				t.Errorf("serviceOwnedByUID() = (%v, %v, %q), want nothing recorded", present, owned, prev)
			}
		})
	}
}

func TestProxySetupRefusesUnreadableService(t *testing.T) {
	t.Run("install", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		unitPath := proxyhost.ServiceUnitPath("linux")
		h.readErrs = map[string]error{unitPath: os.ErrPermission}
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "read service unit "+unitPath) {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 || h.hosts() != hostTestOriginal {
			t.Errorf("an unreadable service reached the privileged step: %d call(s)", len(h.priv))
		}
	})
	t.Run("uninstall with an old ledger", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.priv = nil
		unitPath := proxyhost.ServiceUnitPath("linux")
		h.readErrs = map[string]error{unitPath: os.ErrPermission}
		_, err := h.run("uninstall", "--yes")
		if err == nil || !strings.Contains(err.Error(), "read service unit "+unitPath) {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 {
			t.Error("an unreadable service reached the privileged step")
		}
		if _, ok := h.files[unitPath]; !ok {
			t.Error("the unreadable service was deleted")
		}
	})
}

func TestProxyInstallRevalidatesServiceUnit(t *testing.T) {
	unitPath := proxyhost.ServiceUnitPath("linux")
	t.Run("absent unit", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		if got := h.priv[0].env["JAILOC_UNIT_PREV_SHA256"]; got != "missing" {
			t.Errorf("JAILOC_UNIT_PREV_SHA256 = %q, want missing", got)
		}
	})
	t.Run("installed unit", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.broker = []byte(hostTestBroker + "# v2\n")
		h.priv = nil
		h.mustRun("install", "--yes")
		if len(h.priv) != 1 {
			t.Fatalf("privileged calls = %d, want 1", len(h.priv))
		}
		if got, want := h.priv[0].env["JAILOC_UNIT_PREV_SHA256"], sha256Hex(h.files[unitPath]); got != want {
			t.Errorf("JAILOC_UNIT_PREV_SHA256 = %q, want the hash of the installed unit %q", got, want)
		}
	})
	t.Run("unit appears while waiting", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		foreign, err := proxyhost.RenderSystemdUnit(proxyhost.HelperBinaryPath("linux"), 777)
		if err != nil {
			t.Fatal(err)
		}
		h.beforePriv = func() { h.files[unitPath] = foreign }
		_, err = h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "appeared while waiting for confirmation") {
			t.Fatalf("error = %v", err)
		}
		if !bytes.Equal(h.files[unitPath], foreign) {
			t.Error("the unit of another user was replaced")
		}
		if _, ok := h.files[proxyhost.HelperBinaryPath("linux")]; ok {
			t.Error("the helper was installed next to the unit of another user")
		}
	})
}

func TestProxyUninstallRevalidatesServiceUnit(t *testing.T) {
	unitPath := proxyhost.ServiceUnitPath("linux")
	t.Run("unit as planned", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		want := sha256Hex(h.files[unitPath])
		h.priv = nil
		h.mustRun("uninstall", "--yes")
		if got := h.priv[0].env["JAILOC_UNIT_PREV_SHA256"]; got != want {
			t.Errorf("JAILOC_UNIT_PREV_SHA256 = %q, want %q", got, want)
		}
	})
	t.Run("unit already gone", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		delete(h.files, unitPath)
		h.priv = nil
		h.mustRun("uninstall", "--yes")
		if got := h.priv[0].env["JAILOC_UNIT_PREV_SHA256"]; got != "missing" {
			t.Errorf("JAILOC_UNIT_PREV_SHA256 = %q, want missing", got)
		}
	})
	t.Run("unit replaced while waiting", func(t *testing.T) {
		h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		foreign, err := proxyhost.RenderSystemdUnit(proxyhost.HelperBinaryPath("linux"), 777)
		if err != nil {
			t.Fatal(err)
		}
		h.beforePriv = func() { h.files[unitPath] = foreign }
		_, err = h.run("uninstall", "--yes")
		if err == nil || !strings.Contains(err.Error(), "changed while waiting for confirmation") {
			t.Fatalf("error = %v", err)
		}
		if !bytes.Equal(h.files[unitPath], foreign) {
			t.Error("the unit of another user was removed")
		}
		if _, ok := h.files[proxyhost.HelperBinaryPath("linux")]; !ok {
			t.Error("the helper of another user was removed")
		}
	})
}

// Restarting the attached proxy.

func TestProxyInstallRestartPrintsBannerAndKeepsConfiguration(t *testing.T) {
	newHarness := func(t *testing.T) *hostHarness {
		t.Helper()
		h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
		h.mustRun("install", "--yes")
		h.broker = []byte(hostTestBroker + "# v2\n")
		h.priv = nil
		return h
	}
	t.Run("the startup banner reaches the user", func(t *testing.T) {
		h := newHarness(t)
		h.proxy = &proxyState{ListenerBroker: true, Config: proxyConfigPath(), Workspaces: map[string][]string{"app": nil}}
		h.restartBanner = "Login code for app: 123456\n"
		out := h.mustRun("install", "--yes")
		assertContains(t, out, "Login code for app: 123456")
		assertContains(t, out, "Attached the background proxy to the restarted listener broker.")
	})
	t.Run("a proxy of another configuration is refused before anything changes", func(t *testing.T) {
		h := newHarness(t)
		h.proxy = &proxyState{ListenerBroker: true, Config: "/elsewhere/config.toml", Workspaces: map[string][]string{"app": nil}}
		_, err := h.run("install", "--yes")
		if err == nil || !strings.Contains(err.Error(), "/elsewhere/config.toml") || !strings.Contains(err.Error(), "jailoc proxy stop") {
			t.Fatalf("error = %v", err)
		}
		if len(h.priv) != 0 || len(h.restarted) != 0 {
			t.Errorf("the broker was restarted (%d privileged call(s), %v restarted proxies)", len(h.priv), h.restarted)
		}
	})
	t.Run("a proxy of another configuration is left alone when the broker stays", func(t *testing.T) {
		h := newHarness(t)
		h.mustRun("install", "--yes")
		h.proxy = &proxyState{ListenerBroker: true, Config: "/elsewhere/config.toml", Workspaces: map[string][]string{"app": nil}}
		h.mustRun("install", "--yes")
		if len(h.restarted) != 0 {
			t.Errorf("an unchanged install restarted the proxy: %v", h.restarted)
		}
	})
}

// CA rotation must not report the new CA as trusted where only the old one is.

func TestProxyInstallRotationFailureDoesNotKeepKeychainFlag(t *testing.T) {
	h := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	if !h.installState().MacOSKeychainCert {
		t.Fatal("the first install did not trust the keychain")
	}
	h.rotateCA()
	h.commandErr = func(args []string) error {
		if args[0] == "security" && args[1] == "add-trusted-cert" {
			return errors.New("user canceled")
		}
		return nil
	}
	if _, err := h.run("install", "--yes"); err == nil {
		t.Fatal("want the keychain failure")
	}
	st := h.installState()
	if st.MacOSKeychainCert {
		t.Errorf("the keychain flag of the previous CA was kept for the new one: %+v", st)
	}
	out := h.mustRunStatus()
	assertContains(t, out, "not trusted in any store")
}

func TestProxyInstallRotationRecordsOnlyVerifiedTrust(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	nssDB := h.addNSSDatabase(".pki/nssdb")
	h.mustRun("install", "--yes")
	first := h.installState()
	if first.SystemCACertPath == "" || !slices.Equal(first.NSSDatabases, []string{nssDB}) {
		t.Fatalf("first install state = %+v", first)
	}

	// The trust updates of the rotation are unavailable.
	h.rotateCA()
	h.systemStore, h.certutil = false, false
	h.mustRun("install", "--yes")
	second := h.installState()
	if second.CACertSHA256 == first.CACertSHA256 {
		t.Fatal("the CA was not rotated")
	}
	if len(second.NSSDatabases) != 0 || !slices.Equal(second.PendingNSSDatabases, []string{nssDB}) ||
		!slices.Equal(second.PendingNSSCertSHA256s, []string{first.CACertSHA256}) {
		t.Errorf("NSS state = %v, pending %v / %v; want no database with the new CA and %s pending with the old one",
			second.NSSDatabases, second.PendingNSSDatabases, second.PendingNSSCertSHA256s, nssDB)
	}
	if second.SystemCACertPath == "" || second.SystemCACertSHA256 != first.SystemCACertSHA256 {
		t.Errorf("the recorded system CA location is lost: %+v", second)
	}
	assertContains(t, h.mustRunStatus(), "not trusted in any store")

	// Uninstall removes the old CA from the pending database as well.
	h.certutil = true
	h.commands = nil
	h.mustRun("uninstall", "--yes")
	if !h.hasCommand("certutil", "-d", "sql:"+nssDB, "-D") {
		t.Errorf("the pending database was not cleaned: %v", h.commands)
	}
	if _, err := os.Stat(proxyInstallStatePath()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("install.json still exists (%v)", err)
	}
}

func TestProxyInstallRotationMovesRefreshedDatabasesOutOfPending(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	nssDB := h.addNSSDatabase(".pki/nssdb")
	h.mustRun("install", "--yes")
	h.rotateCA()
	h.mustRun("install", "--yes")
	st := h.installState()
	if !slices.Equal(st.NSSDatabases, []string{nssDB}) || len(st.PendingNSSDatabases) != 0 || len(st.PendingNSSCertSHA256s) != 0 {
		t.Errorf("state = NSS %v, pending %v / %v; want the database with the new CA and nothing pending", st.NSSDatabases, st.PendingNSSDatabases, st.PendingNSSCertSHA256s)
	}
	assertContains(t, h.mustRunStatus(), "(verified: system store; recorded by `jailoc proxy install`, not verified: 1 NSS database(s))")
}

func TestProxyStatusVerifiesSystemStoreAndNSSDatabases(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	nssDB := h.addNSSDatabase(".pki/nssdb")
	h.mustRun("install", "--yes")
	assertContains(t, h.mustRunStatus(), "verified: system store; recorded by `jailoc proxy install`, not verified: 1 NSS database(s)")

	t.Run("system certificate replaced", func(t *testing.T) {
		st := h.installState()
		h.files[st.SystemCACertPath] = []byte("another certificate")
		out := h.mustRunStatus()
		if strings.Contains(out, "system store") {
			t.Errorf("a replaced system certificate is reported as trusted:\n%s", out)
		}
		assertContains(t, out, "1 NSS database(s)")
	})
	t.Run("NSS database deleted", func(t *testing.T) {
		if err := os.RemoveAll(nssDB); err != nil {
			t.Fatal(err)
		}
		out := h.mustRunStatus()
		if strings.Contains(out, "NSS database") {
			t.Errorf("a deleted database is reported as trusted:\n%s", out)
		}
		assertContains(t, out, "not trusted in any store")
	})
}

func (h *hostHarness) mustRunStatus() string {
	h.t.Helper()
	return h.mustRun("status")
}

// Round 4 hardening: validated CA bytes, embedded broker, confirmed service
// stop, unowned trust entries and the status contract.

func TestProxyInstallImportsStagedValidatedCA(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			h := useHostHarness(t, goos, hostTestConfig, hostTestOriginal)
			h.addNSSDatabase(".pki/nssdb")
			h.mustRun("install", "--yes")
			validated := h.installState().CACertPEM

			// ca.crt changes after it was validated, while the install runs.
			h.beforePriv = func() {
				if err := os.WriteFile(proxyCACertPath(), []byte("tampered"), 0o600); err != nil {
					t.Error(err)
				}
			}
			h.broker = []byte(hostTestBroker + "# v2\n") // makes darwin run a privileged step too
			h.imports, h.priv = nil, nil
			h.mustRun("install", "--yes")
			if len(h.priv) == 0 {
				t.Fatal("no privileged step ran, so the CA file was never tampered with")
			}
			if len(h.imports) == 0 {
				t.Fatal("no trust tool imported the CA")
			}
			for _, imp := range h.imports {
				if imp.path == proxyCACertPath() {
					t.Errorf("a trust tool imported the file in the CA directory, not a staged copy")
				}
				if !bytes.Equal(imp.data, validated) {
					t.Errorf("a trust tool imported %q, want the validated certificate", imp.data)
				}
			}
			if src := h.priv[0].files["JAILOC_CA_SRC"]; goos == "linux" && !bytes.Equal(src, validated) {
				t.Errorf("system store got %q, want the validated certificate", src)
			}
		})
	}
}

func TestDefaultProxyBrokerBinaryUsesEmbeddedBytes(t *testing.T) {
	embedded, err := brokerbin.Bytes()
	if errors.Is(err, brokerbin.ErrNotEmbedded) {
		t.Skip("this build embeds no listener broker")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", shortTempDir(t, "bb"))
	got, err := defaultProxyBrokerBinary()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, embedded) {
		t.Error("the broker binary is not the embedded one")
	}
	if _, err := os.Stat(filepath.Join(vmRootDir(), "broker")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the embedded broker was extracted into the cache and read back (%v)", err)
	}
}

func TestReadSiblingBrokerBinary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := filepath.Join(dir, "jailoc")
	if _, err := readSiblingBrokerBinary(exe); err == nil || !strings.Contains(err.Error(), "none was found next to it") {
		t.Errorf("without a sibling: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, brokerbin.FileName), 0o755); err != nil { //nolint:gosec // G301: directory under test
		t.Fatal(err)
	}
	if _, err := readSiblingBrokerBinary(exe); err == nil {
		t.Error("a directory next to the executable was accepted")
	}
	if err := os.Remove(filepath.Join(dir, brokerbin.FileName)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, brokerbin.FileName), []byte(hostTestBroker), 0o700); err != nil { //nolint:gosec // test stub binary
		t.Fatal(err)
	}
	got, err := readSiblingBrokerBinary(exe)
	if err != nil || string(got) != hostTestBroker {
		t.Errorf("sibling = %q, %v", got, err)
	}
}

func TestProxyUninstallKeepsLedgerAndAssetsWhenServiceNotStopped(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	hostsAfterInstall := h.hosts()
	before := h.installState()
	h.privErr = errors.New("jailoc-listener-broker.service is active, not stopped; leaving it installed")
	h.priv = nil

	if _, err := h.run("uninstall", "--yes"); err == nil || !strings.Contains(err.Error(), "not stopped") {
		t.Fatalf("error = %v, want the failure of the script", err)
	}
	st := h.installState()
	if !bytes.Equal(st.HostsLedger, before.HostsLedger) || st.HelperPath != before.HelperPath || st.ServiceUnitPath != before.ServiceUnitPath || st.SystemCACertPath != before.SystemCACertPath {
		t.Errorf("state = %+v, want the ledger and assets kept: %+v", st, before)
	}
	if h.hosts() != hostsAfterInstall {
		t.Error("the hosts block was removed although the service was not confirmed stopped")
	}
	for _, p := range []string{call(h, "JAILOC_HELPER_DST"), call(h, "JAILOC_UNIT_DST"), hostTestSystemCA} {
		if _, ok := h.files[p]; !ok {
			t.Errorf("%s was removed although the service was not confirmed stopped", p)
		}
	}
}

// call is an environment value of the last privileged call.
func call(h *hostHarness, key string) string { return h.priv[len(h.priv)-1].env[key] }

// runUninstallScript runs the uninstall script for goos with stub launchctl,
// systemctl and sleep commands in front of PATH. stubs maps a command to its
// script body; the stubs log their arguments to the file at $STUB_LOG.
// stubFiles are empty files created in the stub directory ($STUB_DIR), the
// state the stubs read.
func runUninstallScript(t *testing.T, goos string, withUnit bool, stubs, env map[string]string, stubFiles ...string) (out string, files map[string]bool, log string) {
	t.Helper()
	stubDir := shortTempDir(t, "st")
	for _, f := range stubFiles {
		if err := os.WriteFile(filepath.Join(stubDir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	rootDir := shortTempDir(t, "ur")
	_, lock := hostLockDirs(t)
	unit := filepath.Join(rootDir, "unit")
	helper := filepath.Join(rootDir, "helper")
	socket := filepath.Join(rootDir, "sock")
	logPath := filepath.Join(stubDir, "log")
	prev := "missing"
	if withUnit {
		if err := os.WriteFile(unit, []byte("unit"), 0o644); err != nil { //nolint:gosec // test unit file
			t.Fatal(err)
		}
		prev = sha256Hex([]byte("unit"))
	}
	for _, f := range []string{helper} {
		if err := os.WriteFile(f, []byte("helper"), 0o755); err != nil { //nolint:gosec // test helper file
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(socket, 0o755); err != nil { //nolint:gosec // G301: test socket directory
		t.Fatal(err)
	}
	stubs["sleep"] = "exit 0"
	for name, body := range stubs {
		script := "#!/bin/sh\necho \"" + name + " $*\" >> \"$STUB_LOG\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(stubDir, name), []byte(script), 0o755); err != nil { //nolint:gosec // G306: test stub command
			t.Fatal(err)
		}
	}
	cmd := exec.CommandContext(t.Context(), "sh", "-e", "-c", proxyUninstallScript(goos, true, false, nil)) //nolint:gosec // G204: constant script under test
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STUB_LOG="+logPath, "STUB_DIR="+stubDir,
		"JAILOC_HOST_LOCK_DIR="+lock,
		"JAILOC_UNIT_DST="+unit, "JAILOC_UNIT_PREV_SHA256="+prev, "JAILOC_HELPER_DST="+helper, "JAILOC_SOCKET_DIR="+socket,
		"JAILOC_SERVICE_LABEL=cz.test.broker", "JAILOC_SERVICE_NAME=test-broker.service",
	)
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	combined, _ := cmd.CombinedOutput()
	logData, _ := os.ReadFile(logPath) //nolint:gosec // G304: log file in a test directory
	files = map[string]bool{}
	for name, p := range map[string]string{"unit": unit, "helper": helper, "socket": socket} {
		_, err := os.Stat(p)
		files[name] = err == nil
	}
	return string(combined), files, string(logData)
}

func TestProxyUninstallScriptConfirmsServiceStoppedLinux(t *testing.T) {
	systemctl := func(extra string) map[string]string {
		return map[string]string{"systemctl": `case "$1" in
disable) exit "${STUB_DISABLE_RC:-0}" ;;
show) [ -z "$STUB_SHOW_RC" ] || exit "$STUB_SHOW_RC"; echo "${STUB_STATE:-inactive}" ;;
esac
` + extra}
	}
	t.Run("stopped service is removed", func(t *testing.T) {
		out, files, log := runUninstallScript(t, "linux", true, systemctl(""), nil)
		if files["unit"] || files["helper"] || files["socket"] || !strings.Contains(log, "systemctl disable --now test-broker.service") {
			t.Errorf("files %v, log %q, output %q; want everything removed after a stop", files, log, out)
		}
	})
	t.Run("absent service is confirmed absent", func(t *testing.T) {
		out, files, log := runUninstallScript(t, "linux", false, systemctl(""), nil)
		if files["helper"] || strings.Contains(log, "disable") {
			t.Errorf("files %v, log %q, output %q; want the leftovers removed without disabling a unit that is not there", files, log, out)
		}
	})
	for name, env := range map[string]map[string]string{
		"disable fails":      {"STUB_DISABLE_RC": "1"},
		"still active":       {"STUB_STATE": "active"},
		"state unreadable":   {"STUB_SHOW_RC": "1"},
		"restarting service": {"STUB_STATE": "activating"},
	} {
		t.Run(name+" keeps the files", func(t *testing.T) {
			out, files, _ := runUninstallScript(t, "linux", true, systemctl(""), env)
			if !files["unit"] || !files["helper"] || !files["socket"] {
				t.Errorf("files %v, output %q; want every file kept when the stop is not confirmed", files, out)
			}
		})
	}
}

func TestProxyUninstallScriptConfirmsServiceStoppedDarwin(t *testing.T) {
	launchctl := func() map[string]string {
		return map[string]string{"launchctl": `case "$1" in
print)
	[ -z "$STUB_PRINT_RC" ] || exit "$STUB_PRINT_RC"
	[ -e "$STUB_DIR/loaded" ] || exit 113 ;;
bootout)
	[ -z "$STUB_BOOTOUT_RC" ] || exit "$STUB_BOOTOUT_RC"
	[ -n "$STUB_STAY_LOADED" ] || rm -f "$STUB_DIR/loaded" ;;
esac`}
	}
	t.Run("loaded service is booted out", func(t *testing.T) {
		out, files, log := runUninstallScript(t, "darwin", true, launchctl(), nil, "loaded")
		if files["unit"] || files["helper"] || !strings.Contains(log, "launchctl bootout system/cz.test.broker") {
			t.Errorf("files %v, log %q, output %q", files, log, out)
		}
	})
	t.Run("service launchd does not know is absent", func(t *testing.T) {
		out, files, log := runUninstallScript(t, "darwin", true, launchctl(), nil)
		if files["unit"] || files["helper"] || strings.Contains(log, "bootout") {
			t.Errorf("files %v, log %q, output %q", files, log, out)
		}
	})
	for name, env := range map[string]map[string]string{
		"bootout fails":          {"STUB_BOOTOUT_RC": "5"},
		"service stays loaded":   {"STUB_STAY_LOADED": "1"},
		"launchctl print failed": {"STUB_PRINT_RC": "5"},
	} {
		t.Run(name+" keeps the files", func(t *testing.T) {
			out, files, _ := runUninstallScript(t, "darwin", true, launchctl(), env, "loaded")
			if !files["unit"] || !files["helper"] || !files["socket"] {
				t.Errorf("files %v, output %q; want every file kept when the stop is not confirmed", files, out)
			}
		})
	}
}

func TestProxyInstallLeavesPreexistingSystemCAAlone(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	// A system CA the install state does not record: someone else put the
	// same certificate there.
	st := h.installState()
	existing := slices.Clone(h.files[hostTestSystemCA])
	st.SystemCACertPath, st.SystemCACertSHA256, st.SystemCAUpdateCmd, st.SystemCARemoveCmd = "", "", nil, nil
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	h.priv = nil

	out := h.mustRun("install", "--yes")
	if len(h.priv) != 0 {
		t.Errorf("the unrecorded system CA reached a privileged step: %v", h.priv[0].script)
	}
	if !bytes.Equal(h.files[hostTestSystemCA], existing) {
		t.Error("the unrecorded system CA was modified")
	}
	if got := h.installState(); got.SystemCACertPath != "" || got.SystemCACertSHA256 != "" {
		t.Errorf("state = %+v, want the unrecorded system CA not adopted", got)
	}
	assertContains(t, out, "jailoc did not install it and leaves it as it is")
	if strings.Contains(h.mustRunStatus(), "verified: system store") {
		t.Error("status verifies a system CA jailoc does not own")
	}

	h.priv = nil
	h.mustRun("uninstall", "--yes")
	if !bytes.Equal(h.files[hostTestSystemCA], existing) {
		t.Error("uninstall removed a system CA jailoc did not install")
	}
	for _, c := range h.priv {
		if strings.Contains(c.script, `remove_ca_file "$JAILOC_CA_DST"`) {
			t.Errorf("uninstall script removes the unrecorded system CA:\n%s", c.script)
		}
	}
}

func TestProxyInstallLeavesUnrecordedNSSEntriesAlone(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	caPEM := h.installState().CACertPEM
	db := h.addNSSDatabase(".pki/nssdb")
	h.useNSSInspection(map[string][]byte{db: caPEM})
	h.commands = nil

	out := h.mustRun("install", "--yes")
	if h.hasCommand("certutil", "-d", "sql:"+db) && (h.hasCommand("certutil", "-d", "sql:"+db, "-M") || h.hasCommand("certutil", "-d", "sql:"+db, "-A") || h.hasCommand("certutil", "-d", "sql:"+db, "-D")) {
		t.Errorf("an NSS entry jailoc did not add was modified: %v", h.commands)
	}
	if st := h.installState(); slices.Contains(st.NSSDatabases, db) || slices.Contains(st.PendingNSSDatabases, db) {
		t.Errorf("state = %+v, want the unrecorded database not adopted", st)
	}
	assertContains(t, out, "jailoc did not install it there")

	h.commands = nil
	h.mustRun("uninstall", "--yes")
	if h.hasCommand("certutil", "-d", "sql:"+db) {
		t.Errorf("uninstall touched a database it never recorded: %v", h.commands)
	}
}

func TestProxyInstallPendingNSSOwnershipIsPerDatabase(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.mustRun("install", "--yes")
	recordedDB := h.addNSSDatabase(".mozilla/firefox/recorded.default")
	otherDB := h.addNSSDatabase(".mozilla/firefox/other.default")
	leftover, leftoverSHA := testCertPEM(t, "jailoc proxy CA before the last rotation")
	st := h.installState()
	st.PendingNSSDatabases = []string{recordedDB}
	st.PendingNSSCertSHA256s = []string{leftoverSHA}
	if err := proxyhost.WriteInstallState(proxyInstallStatePath(), st); err != nil {
		t.Fatal(err)
	}
	// The same recorded fingerprint sits in a database install never recorded.
	h.useNSSInspection(map[string][]byte{recordedDB: leftover, otherDB: leftover})
	h.commands = nil

	out := h.mustRun("install", "--yes")
	if !h.hasCommand("certutil", "-d", "sql:"+recordedDB, "-D") || !h.hasCommand("certutil", "-d", "sql:"+recordedDB, "-A") {
		t.Errorf("the recorded old CA in its recorded database was not replaced: %v", h.commands)
	}
	if h.hasCommand("certutil", "-d", "sql:"+otherDB, "-D") || h.hasCommand("certutil", "-d", "sql:"+otherDB, "-A") {
		t.Errorf("a certificate in a database jailoc did not record was replaced: %v", h.commands)
	}
	assertContains(t, out, "refusing to replace existing certificate \"jailoc proxy CA\" in "+otherDB)
	if got := h.installState(); slices.Contains(got.NSSDatabases, otherDB) {
		t.Errorf("state = %+v, want the unrecorded database not adopted", got)
	}
}

func TestProxyStatusSeparatesRecordedFromVerifiedTrust(t *testing.T) {
	h := useHostHarness(t, "linux", hostTestConfig, hostTestOriginal)
	h.addNSSDatabase(".pki/nssdb")
	h.mustRun("install", "--yes")
	out := h.mustRunStatus()
	assertContains(t, out, "verified: system store; recorded by `jailoc proxy install`, not verified: 1 NSS database(s)")
	if strings.Contains(out, "verified: system store, 1 NSS") || strings.Contains(out, "trusted by") {
		t.Errorf("recorded NSS trust is presented as verified:\n%s", out)
	}

	h2 := useHostHarness(t, "darwin", hostTestConfig, hostTestOriginal)
	h2.mustRun("install", "--yes")
	out = h2.mustRunStatus()
	assertContains(t, out, "(recorded by `jailoc proxy install`, not verified: login keychain)")
	if strings.Contains(out, "(verified:") {
		t.Errorf("the keychain was not looked into but is reported as verified:\n%s", out)
	}

	// A ca.crt with more than the certificate is not read as the CA.
	other, _ := testCertPEM(t, "an extra certificate")
	homeRoot, err := os.OpenRoot(h2.home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := homeRoot.Close(); err != nil {
			t.Error(err)
		}
	})
	const caPath = ".local/share/jailoc/proxy/ca.crt"
	data, err := homeRoot.ReadFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := homeRoot.WriteFile(caPath, append(data, other...), 0o600); err != nil {
		t.Fatal(err)
	}
	assertContains(t, h2.mustRunStatus(), "(unreadable)")
}
