//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // G505: macOS `security delete-certificate -Z` identifies certificates by their SHA-1
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/seznam/jailoc/internal/brokerbin"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

const (
	proxyKeychainName   = "login.keychain-db"
	proxyBrokerWait     = 5 * time.Second
	proxyBrokerPoll     = 200 * time.Millisecond
	proxySocketDirRoot  = "/var/run/jailoc-proxy/"
	proxyInstallStateFn = "install.json"
	proxyRootCACommon   = "jailoc proxy root CA"
)

// proxyHostDeps is every seam `jailoc proxy install`, `uninstall` and
// `status` use to touch the host. Tests replace currentProxyHostDeps, which
// makes them non-parallel, so that no test needs root.
type proxyHostDeps struct {
	goos      string
	uid       func() uint32
	homeDir   func() (string, error)
	hostsPath string
	// brokerBinary returns the listener broker binary to install and a
	// cleanup function.
	brokerBinary func() (string, func(), error)
	dirExists    func(string) bool
	lookPath     func(string) (string, error)
	// runPrivileged runs script as root with env set in its environment. The
	// script is constant text: paths travel in env, never in the script.
	runPrivileged func(ctx context.Context, script string, env []string) error
	// runCommand runs a command as the current user.
	runCommand  func(ctx context.Context, name string, args ...string) error
	probeBroker func(ctx context.Context) error
	// interactive reports whether questions can be asked on the terminal.
	interactive func() bool
	// prompt builds the prompter for the confirmation.
	prompt func(yes bool) *promptIO
}

var currentProxyHostDeps = defaultProxyHostDeps

var defaultProxyHostDeps = proxyHostDeps{
	goos:          runtime.GOOS,
	uid:           func() uint32 { return uint32(os.Getuid()) }, //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
	homeDir:       os.UserHomeDir,
	hostsPath:     "/etc/hosts",
	brokerBinary:  defaultProxyBrokerBinary,
	dirExists:     func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
	lookPath:      exec.LookPath,
	runPrivileged: runProxyPrivileged,
	runCommand:    runProxyUserCommand,
	probeBroker: func(ctx context.Context) error {
		t := proxyBrokerHost()
		return proxyhost.ProbeBroker(ctx, t.socketPath, t.brokerUID, t.addr)
	},
	interactive: func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }, //nolint:gosec // G115: file descriptors fit in int
	prompt:      newStdinPromptIO,
}

// defaultProxyBrokerBinary returns the embedded listener broker binary
// extracted into the cache, or the one next to the jailoc executable.
func defaultProxyBrokerBinary() (string, func(), error) {
	noop := func() {}
	path, err := brokerbin.Path(filepath.Join(vmRootDir(), "broker"))
	if err == nil {
		return path, noop, nil
	}
	if !errors.Is(err, brokerbin.ErrNotEmbedded) {
		return "", nil, fmt.Errorf("extract listener broker: %w", err)
	}
	exe, exeErr := os.Executable()
	if exeErr != nil {
		return "", nil, fmt.Errorf("locate jailoc executable: %w", exeErr)
	}
	sibling := filepath.Join(filepath.Dir(exe), brokerbin.FileName)
	if fi, statErr := os.Stat(sibling); statErr != nil || !fi.Mode().IsRegular() {
		return "", nil, fmt.Errorf("this jailoc build has no embedded %s and none was found next to it (%s); use a release build or build it with `go build ./cmd/jailoc-listener-broker`", brokerbin.FileName, sibling)
	}
	return sibling, noop, nil
}

// runProxyPrivileged runs script as root through sudo. The variables are set
// by env(1) after sudo reset the environment, so sudoers need not allow them.
func runProxyPrivileged(ctx context.Context, script string, env []string) error {
	args := make([]string, 0, len(env)+7)
	args = append(args, "--", "env")
	args = append(args, env...)
	args = append(args, "sh", "-e", "-c", script)
	cmd := exec.CommandContext(ctx, "sudo", args...) //nolint:gosec // G204: constant script, paths in env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo: %w", err)
	}
	return nil
}

func runProxyUserCommand(ctx context.Context, name string, args ...string) error {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: fixed tools with jailoc-derived arguments
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// proxyInstallStatePath is where `jailoc proxy install` records what it
// changed.
func proxyInstallStatePath() string {
	return filepath.Join(proxyCADir(), proxyInstallStateFn)
}

// proxyHostsFilePath is the hosts file the proxy host names must be in.
func proxyHostsFilePath() string {
	return currentProxyHostDeps.hostsPath
}

// workspaceProxyHostNames are the host names of ws: the bare workspace name
// and one per route. A workspace without routes has none.
func workspaceProxyHostNames(ws *workspace.Resolved) []proxyhost.Name {
	if len(ws.Proxy) == 0 {
		return nil
	}
	names := make([]proxyhost.Name, 0, len(ws.Proxy)+1)
	names = append(names, proxyhost.Name{Workspace: ws.Name})
	for _, r := range ws.Proxy {
		names = append(names, proxyhost.Name{Workspace: ws.Name, Route: r.Name})
	}
	return names
}

// desiredProxyHostNames are the host names of every configured workspace
// that has proxy routes.
func desiredProxyHostNames(cfg *config.Config) ([]proxyhost.Name, error) {
	var names []proxyhost.Name
	for _, name := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
		ws, err := workspace.Resolve(cfg, name)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %q: %w", name, err)
		}
		if len(ws.Proxy) == 0 {
			continue
		}
		names = append(names, workspaceProxyHostNames(ws)...)
	}
	return names, nil
}

// loadProxyHostConfig loads the configuration, or an empty one when the file
// does not exist.
func loadProxyHostConfig() (*config.Config, error) {
	if _, err := os.Stat(config.ConfigPath()); errors.Is(err, os.ErrNotExist) {
		return &config.Config{}, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func readProxyHostsFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: the system hosts file
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

const proxyPrivilegedHelpers = `hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}
# install_file MODE SRC DST SHA256: copy SRC next to DST as root, check that
# the copy is the file jailoc hashed, and only then move it into place, so a
# file replaced after jailoc prepared it is never installed as root.
install_file() {
	[ -d "$(dirname "$3")" ] || install -d -m 0755 -o 0 -g 0 "$(dirname "$3")"
	install -m "$1" -o 0 -g 0 "$2" "$3.jailoc-new"
	if [ "$(hash_file "$3.jailoc-new")" != "$4" ]; then
		rm -f "$3.jailoc-new"
		echo "jailoc: $2 changed while it was being installed; nothing was installed" >&2
		exit 1
	fi
	mv -f "$3.jailoc-new" "$3"
}
# write_hosts SRC SHA256 DST: replace the content of DST (keeping a symlink
# in place) with the verified SRC.
write_hosts() {
	tmp="$(mktemp)"
	cat "$1" > "$tmp"
	if [ "$(hash_file "$tmp")" != "$2" ]; then
		rm -f "$tmp"
		echo "jailoc: $1 changed while it was being installed; hosts file untouched" >&2
		exit 1
	fi
	cat "$tmp" > "$3"
	rm -f "$tmp"
}
`

// proxyInstallScript is the privileged part of `jailoc proxy install`.
func proxyInstallScript(goos string, withHosts bool, store *proxyhost.LinuxCAStore) string {
	var b strings.Builder
	b.WriteString(proxyPrivilegedHelpers)
	b.WriteString(`install_file 0755 "$JAILOC_HELPER_SRC" "$JAILOC_HELPER_DST" "$JAILOC_HELPER_SHA256"` + "\n")
	b.WriteString(`install_file 0644 "$JAILOC_UNIT_SRC" "$JAILOC_UNIT_DST" "$JAILOC_UNIT_SHA256"` + "\n")
	if withHosts {
		b.WriteString(`write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"` + "\n")
	}
	if store != nil {
		b.WriteString(`install_file 0644 "$JAILOC_CA_SRC" "$JAILOC_CA_DST" "$JAILOC_CA_SHA256"` + "\n")
		b.WriteString(shellJoin(store.UpdateCmd) + "\n")
	}
	switch goos {
	case "darwin":
		b.WriteString(`launchctl bootout "system/$JAILOC_SERVICE_LABEL" >/dev/null 2>&1 || true` + "\n")
		b.WriteString(`launchctl bootstrap system "$JAILOC_UNIT_DST"` + "\n")
		b.WriteString(`launchctl kickstart -k "system/$JAILOC_SERVICE_LABEL"` + "\n")
	default:
		b.WriteString("systemctl daemon-reload\n")
		b.WriteString(`systemctl enable "$JAILOC_SERVICE_NAME"` + "\n")
		b.WriteString(`systemctl restart "$JAILOC_SERVICE_NAME"` + "\n")
	}
	return b.String()
}

// proxyUninstallScript is the privileged part of `jailoc proxy uninstall`.
func proxyUninstallScript(goos string, withHosts bool, store *proxyhost.LinuxCAStore) string {
	var b strings.Builder
	b.WriteString(proxyPrivilegedHelpers)
	switch goos {
	case "darwin":
		b.WriteString(`launchctl bootout "system/$JAILOC_SERVICE_LABEL" >/dev/null 2>&1 || true` + "\n")
		b.WriteString(`rm -f "$JAILOC_UNIT_DST"` + "\n")
	default:
		b.WriteString(`systemctl disable --now "$JAILOC_SERVICE_NAME" >/dev/null 2>&1 || true` + "\n")
		b.WriteString(`rm -f "$JAILOC_UNIT_DST"` + "\n")
		b.WriteString("systemctl daemon-reload\n")
	}
	b.WriteString(`rm -f "$JAILOC_HELPER_DST"` + "\n")
	b.WriteString(`rm -rf "$JAILOC_SOCKET_DIR"` + "\n")
	if withHosts {
		b.WriteString(`write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"` + "\n")
	}
	if store != nil {
		b.WriteString(`rm -f "$JAILOC_CA_DST"` + "\n")
		b.WriteString(shellJoin(store.RemoveCmd) + "\n")
	}
	return b.String()
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// caFingerprints returns the SHA-256 and SHA-1 of a DER certificate.
func caFingerprints(der []byte) (sha256hex, sha1hex string) {
	s1 := sha1.Sum(der) //nolint:gosec // G401: certificate identifier, not a security hash
	return sha256Hex(der), strings.ToUpper(hex.EncodeToString(s1[:]))
}

// pemSHA1 returns the SHA-1 of the first certificate in a PEM, or "".
func pemSHA1(data []byte) string {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return ""
	}
	_, s1 := caFingerprints(block.Bytes)
	return s1
}

// checkProxyHostInvocation refuses to run as root and without a way to ask.
func checkProxyHostInvocation(d proxyHostDeps, verb string, yes bool) error {
	if d.uid() == 0 {
		return fmt.Errorf("refusing to run proxy %s as root; run it as your own user", verb)
	}
	if !yes && !d.interactive() && !isAccessibleEnv() {
		return fmt.Errorf("proxy %s asks for confirmation; run it in a terminal or pass --yes", verb)
	}
	if proxyhost.HelperBinaryPath(d.goos) == "" {
		return fmt.Errorf("proxy %s is not supported on %s", verb, d.goos)
	}
	return nil
}

func proxyHostContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

func newProxyInstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up the host for the proxy: listener broker, /etc/hosts and CA trust",
		Long: "Set up the host so that https://<route>.<workspace>.jailoc.local works in a browser without a port.\n\n" +
			"After one confirmation, install uses sudo to install jailoc-listener-broker as a root service that holds\n" +
			"127.0.0.1:443 and [::1]:443 for your user (a LaunchDaemon on macOS, a systemd unit on Linux), and to add a\n" +
			"marked block with the proxy host names to /etc/hosts. It then trusts the jailoc proxy CA, which is limited to\n" +
			"names under jailoc.local, in the macOS login keychain, the Linux system store and the NSS databases of Firefox\n" +
			"and Chrome that it finds. What it changed is recorded in ~/.local/share/jailoc/proxy/install.json, so that\n" +
			"`jailoc proxy uninstall` reverts exactly that. Run it again after adding proxy routes to update /etc/hosts.\n" +
			"Run it as your own user, not as root.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyInstall(cmd, yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newProxyUninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Revert the host setup of `jailoc proxy install`",
		Long: "Stop the proxy and remove what `jailoc proxy install` set up: the listener broker service and binary, the\n" +
			"proxy block in /etc/hosts and the trust of the proxy CA. Everything else in /etc/hosts stays byte for byte.\n" +
			"The CA files in ~/.local/share/jailoc/proxy are kept. Run it as your own user, not as root.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyUninstall(cmd, yes)
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// stageProxyFile writes data into the private staging directory dir and
// returns its path and SHA-256.
func stageProxyFile(dir, name string, data []byte) (path, sum string, err error) {
	path = filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", fmt.Errorf("stage %s: %w", name, err)
	}
	return path, sha256Hex(data), nil
}

func runProxyInstall(cmd *cobra.Command, yes bool) error {
	d := currentProxyHostDeps
	ctx := proxyHostContext(cmd)
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := checkProxyHostInvocation(d, "install", yes); err != nil {
		return err
	}
	helperPath := proxyhost.HelperBinaryPath(d.goos)
	unitPath := proxyhost.ServiceUnitPath(d.goos)
	uid := d.uid()
	home, err := d.homeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	cfg, err := loadProxyHostConfig()
	if err != nil {
		return err
	}
	caPath, root, rootPEM, err := ensureProxyCA(cfg)
	if err != nil {
		return err
	}
	caSHA256, _ := caFingerprints(root.Raw)

	statePath := proxyInstallStatePath()
	prev, err := proxyhost.ReadInstallState(statePath)
	if err != nil {
		return err
	}
	desired, err := desiredProxyHostNames(cfg)
	if err != nil {
		return err
	}
	hostsCur, err := readProxyHostsFile(d.hostsPath)
	if err != nil {
		return err
	}
	plan, err := proxyhost.PlanHosts(hostsCur, proxyhost.Ledger{Owned: prev.HostsLedger}, desired)
	if err != nil {
		return fmt.Errorf("plan %s: %w (remove the jailoc block by hand or restore %s, then run install again)", d.hostsPath, err, statePath)
	}
	hostsChanged := !bytes.Equal(plan.Content, hostsCur)

	var unit []byte
	if d.goos == "darwin" {
		unit, err = proxyhost.RenderLaunchDaemonPlist(helperPath, uid)
	} else {
		unit, err = proxyhost.RenderSystemdUnit(helperPath, uid)
	}
	if err != nil {
		return err
	}

	var store *proxyhost.LinuxCAStore
	if d.goos == "linux" {
		if s, ok := proxyhost.DetectLinuxCAStore(d.dirExists, d.lookPath); ok {
			store = &s
		}
	}

	brokerSrc, cleanup, err := d.brokerBinary()
	if err != nil {
		return err
	}
	defer cleanup()
	brokerData, err := os.ReadFile(brokerSrc) //nolint:gosec // G304: path from the broker binary seam
	if err != nil {
		return fmt.Errorf("read listener broker binary: %w", err)
	}

	stage, err := os.MkdirTemp("", "jailoc-proxy-install-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0o700); err != nil { //nolint:gosec // staging directory must be 0700
		return fmt.Errorf("chmod staging directory: %w", err)
	}
	env := []string{
		"JAILOC_HELPER_DST=" + helperPath,
		"JAILOC_UNIT_DST=" + unitPath,
		"JAILOC_SERVICE_LABEL=" + proxyhost.LaunchDaemonLabel,
		"JAILOC_SERVICE_NAME=" + proxyhost.SystemdServiceName,
	}
	stageEnv := func(name, srcKey, shaKey string, data []byte) error {
		p, sum, err := stageProxyFile(stage, name, data)
		if err != nil {
			return err
		}
		env = append(env, srcKey+"="+p, shaKey+"="+sum)
		return nil
	}
	if err := stageEnv("helper", "JAILOC_HELPER_SRC", "JAILOC_HELPER_SHA256", brokerData); err != nil {
		return err
	}
	if err := stageEnv("service-unit", "JAILOC_UNIT_SRC", "JAILOC_UNIT_SHA256", unit); err != nil {
		return err
	}
	if hostsChanged {
		if err := stageEnv("hosts", "JAILOC_HOSTS_SRC", "JAILOC_HOSTS_SHA256", plan.Content); err != nil {
			return err
		}
		env = append(env, "JAILOC_HOSTS_DST="+d.hostsPath)
	}
	if store != nil {
		if err := stageEnv("ca.crt", "JAILOC_CA_SRC", "JAILOC_CA_SHA256", rootPEM); err != nil {
			return err
		}
		env = append(env, "JAILOC_CA_DST="+store.CertPath)
	}

	nssDBs, nssErr := proxyhost.DiscoverNSSDatabases(d.goos, home)
	_, certutilErr := d.lookPath("certutil")

	summary := proxyInstallSummary(d, home, helperPath, unitPath, hostsChanged, len(desired), plan, store, nssDBs, certutilErr == nil)
	p := d.prompt(yes)
	ok, err := p.confirm(ctx, "Install host support for jailoc proxy?", summary, true)
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprintln(out, "Aborted.")
		return nil
	}

	if err := d.runPrivileged(ctx, proxyInstallScript(d.goos, hostsChanged, store), env); err != nil {
		return fmt.Errorf("install the privileged part of the proxy host setup: %w", err)
	}

	state := proxyhost.InstallState{
		HostsLedger:       plan.Ledger.Owned,
		CACertSHA256:      caSHA256,
		CACertPEM:         rootPEM,
		MacOSKeychainCert: prev.MacOSKeychainCert,
		NSSDatabases:      prev.NSSDatabases,
		HelperPath:        helperPath,
		ServiceUnitPath:   unitPath,
	}
	if store != nil {
		state.SystemCACertPath = store.CertPath
		state.SystemCAUpdateCmd = store.UpdateCmd
		state.SystemCARemoveCmd = store.RemoveCmd
	}
	if err := proxyhost.WriteInstallState(statePath, state); err != nil {
		return err
	}
	if err := writeProxySavedOptions(proxyOptions{listenerBroker: true}); err != nil {
		return fmt.Errorf("save proxy options: %w", err)
	}
	_, _ = fmt.Fprintf(out, "Installed %s and its service (%s).\n", helperPath, unitPath)
	if hostsChanged {
		_, _ = fmt.Fprintf(out, "Updated %s.\n", d.hostsPath)
	}

	trustErr := trustProxyCA(ctx, d, out, errw, home, caPath, caSHA256, prev, &state, nssDBs, nssErr, store, certutilErr == nil)
	if err := proxyhost.WriteInstallState(statePath, state); err != nil {
		return errors.Join(trustErr, err)
	}

	if st := runningProxy(); st != nil && !st.ListenerBroker {
		if err := stopBackgroundProxy(); err != nil {
			_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not stop the background proxy (%v); run `jailoc proxy stop`\n", err)
		} else {
			_, _ = fmt.Fprintln(out, "Stopped the background proxy; the next `jailoc up` or `jailoc proxy` attaches to the listener broker.")
		}
	}
	if err := waitProxyBroker(ctx, d); err != nil {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: the listener broker does not answer yet (%v); check `jailoc proxy status`\n", err)
	} else {
		_, _ = fmt.Fprintln(out, "Listener broker is running. Start a workspace with proxy routes, then open https://<workspace>.jailoc.local/.")
	}
	return trustErr
}

func waitProxyBroker(ctx context.Context, d proxyHostDeps) error {
	deadline := time.Now().Add(proxyBrokerWait)
	for {
		err := d.probeBroker(ctx)
		if err == nil {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(proxyBrokerPoll):
		}
	}
}

func proxyInstallSummary(d proxyHostDeps, home, helperPath, unitPath string, hostsChanged bool, desiredCount int, plan proxyhost.Plan, store *proxyhost.LinuxCAStore, nssDBs []string, haveCertutil bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "As root (sudo), jailoc will:\n")
	fmt.Fprintf(&b, "  - install the listener broker %s (127.0.0.1:443 and [::1]:443 for uid %d)\n", helperPath, d.uid())
	fmt.Fprintf(&b, "  - install and start its service %s\n", unitPath)
	switch {
	case hostsChanged && len(plan.Ledger.Owned) > 0:
		fmt.Fprintf(&b, "  - write %d proxy host name block(s) to %s (127.0.0.1 and ::1 records inside a marked block)\n", desiredCount, d.hostsPath)
	case hostsChanged:
		fmt.Fprintf(&b, "  - remove the proxy block from %s\n", d.hostsPath)
	default:
		fmt.Fprintf(&b, "  - leave %s as it is (%d proxy host name(s) already in place or none needed)\n", d.hostsPath, desiredCount)
	}
	if store != nil {
		fmt.Fprintf(&b, "  - trust the proxy CA in the system store (%s, then %s)\n", store.CertPath, strings.Join(store.UpdateCmd, " "))
	}
	b.WriteString("As you, jailoc will trust the proxy CA (limited to names under jailoc.local) in:\n")
	if d.goos == "darwin" {
		fmt.Fprintf(&b, "  - the macOS login keychain %s\n", shortenHome(filepath.Join(home, "Library", "Keychains", proxyKeychainName), home))
	}
	switch {
	case len(nssDBs) > 0 && haveCertutil:
		fmt.Fprintf(&b, "  - %d NSS database(s) (Firefox/Chrome) with certutil\n", len(nssDBs))
	case len(nssDBs) > 0:
		fmt.Fprintf(&b, "  - nothing in %d NSS database(s): certutil is not installed, you would import the CA by hand\n", len(nssDBs))
	}
	if d.goos == "linux" && store == nil {
		b.WriteString("  - nothing in the Linux system store: no supported store found, you would trust the CA by hand\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// trustProxyCA trusts the CA for the current user and records it in state.
// A failure of the macOS keychain is returned; NSS and system store gaps are
// reported with manual instructions.
func trustProxyCA(ctx context.Context, d proxyHostDeps, out, errw io.Writer, home, caPath, caSHA256 string,
	prev proxyhost.InstallState, state *proxyhost.InstallState, nssDBs []string, nssErr error, store *proxyhost.LinuxCAStore, haveCertutil bool,
) error {
	var result error
	warn := func(format string, a ...any) {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: "+format+"\n", a...)
	}
	if d.goos == "darwin" {
		keychain := filepath.Join(home, "Library", "Keychains", proxyKeychainName)
		if prev.MacOSKeychainCert && prev.CACertSHA256 != "" && prev.CACertSHA256 != caSHA256 {
			if err := removeKeychainCA(ctx, d, keychain, prev); err != nil {
				warn("could not remove the previous proxy CA from the login keychain: %v", err)
			}
		}
		if err := d.runCommand(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, caPath); err != nil {
			result = fmt.Errorf("trust the proxy CA in the login keychain: %w (trust %s by hand in Keychain Access)", err, caPath)
		} else {
			state.MacOSKeychainCert = true
			_, _ = fmt.Fprintln(out, "Trusted the proxy CA in the macOS login keychain.")
		}
	} else if store != nil {
		_, _ = fmt.Fprintf(out, "Trusted the proxy CA in the system store (%s).\n", store.CertPath)
	} else {
		warn("no supported Linux system trust store found; trust %s by hand (for example copy it to your distribution's CA directory and run its update tool)", caPath)
	}

	if nssErr != nil {
		warn("could not look for Firefox and Chrome certificate databases: %v", nssErr)
	}
	added := slices.Clone(prev.NSSDatabases)
	var manual []string
	for _, db := range nssDBs {
		if !haveCertutil {
			manual = append(manual, db)
			continue
		}
		spec := "sql:" + db
		_ = d.runCommand(ctx, "certutil", "-d", spec, "-D", "-n", proxyhost.NSSCertNickname) // best effort: absent on first install
		if err := d.runCommand(ctx, "certutil", "-d", spec, "-A", "-t", "C,,", "-n", proxyhost.NSSCertNickname, "-i", caPath); err != nil {
			warn("could not add the proxy CA to %s: %v", db, err)
			manual = append(manual, db)
			continue
		}
		added = append(added, db)
	}
	state.NSSDatabases = slices.Compact(slices.Sorted(slices.Values(added)))
	if len(state.NSSDatabases) > 0 {
		_, _ = fmt.Fprintf(out, "Trusted the proxy CA in %d NSS database(s).\n", len(state.NSSDatabases))
	}
	if len(manual) > 0 {
		warn("import %s into these Firefox/Chrome databases by hand (install certutil from nss-tools/libnss3-tools and run `jailoc proxy install` again, or use the browser's certificate settings): %s", caPath, strings.Join(manual, ", "))
	}
	if d.goos == "linux" && !slices.Contains(nssDBs, filepath.Join(home, ".pki", "nssdb")) {
		_, _ = fmt.Fprintf(out, "Chrome and Chromium on Linux read ~/.pki/nssdb, not the system store: if you use them, import %s in chrome://settings/certificates (Authorities).\n", caPath)
	}
	return result
}

// removeKeychainCA deletes the CA recorded in st from the keychain, with its
// trust settings.
func removeKeychainCA(ctx context.Context, d proxyHostDeps, keychain string, st proxyhost.InstallState) error {
	if s1 := pemSHA1(st.CACertPEM); s1 != "" {
		return d.runCommand(ctx, "security", "delete-certificate", "-Z", s1, "-t", keychain)
	}
	return d.runCommand(ctx, "security", "delete-certificate", "-c", proxyRootCACommon, "-t", keychain)
}

// trustedSystemCAStore returns the system store layout recorded in st, only
// when its certificate path is one jailoc itself would use: install.json is
// writable by the user, and the uninstall runs as root.
func trustedSystemCAStore(st proxyhost.InstallState) (*proxyhost.LinuxCAStore, bool) {
	if st.SystemCACertPath == "" {
		return nil, false
	}
	dir := filepath.Dir(st.SystemCACertPath)
	store, ok := proxyhost.DetectLinuxCAStore(
		func(p string) bool { return p == dir },
		func(string) (string, error) { return "", nil },
	)
	if !ok || store.CertPath != st.SystemCACertPath {
		return nil, false
	}
	return &store, true
}

func runProxyUninstall(cmd *cobra.Command, yes bool) error {
	d := currentProxyHostDeps
	ctx := proxyHostContext(cmd)
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := checkProxyHostInvocation(d, "uninstall", yes); err != nil {
		return err
	}
	helperPath := proxyhost.HelperBinaryPath(d.goos)
	unitPath := proxyhost.ServiceUnitPath(d.goos)
	uid := d.uid()
	home, err := d.homeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	statePath := proxyInstallStatePath()
	prev, err := proxyhost.ReadInstallState(statePath)
	if err != nil {
		return err
	}
	hostsCur, err := readProxyHostsFile(d.hostsPath)
	if err != nil {
		return err
	}
	var hostsNew []byte
	hostsChanged := false
	if len(prev.HostsLedger) > 0 {
		plan, err := proxyhost.PlanHosts(hostsCur, proxyhost.Ledger{Owned: prev.HostsLedger}, nil)
		if err != nil {
			return fmt.Errorf("revert %s: %w (remove the jailoc block by hand, then run uninstall again)", d.hostsPath, err)
		}
		hostsNew = plan.Content
		hostsChanged = !bytes.Equal(hostsNew, hostsCur)
	}
	var store *proxyhost.LinuxCAStore
	if d.goos == "linux" {
		if s, ok := trustedSystemCAStore(prev); ok {
			store = s
		} else if prev.SystemCACertPath != "" {
			_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: ignoring the system CA path %q recorded in %s: it is not a path jailoc installs to\n", prev.SystemCACertPath, statePath)
		}
	}
	socketDir := filepath.Dir(proxyhost.BrokerSocketPath(uid))
	if !strings.HasPrefix(socketDir, proxySocketDirRoot) {
		return fmt.Errorf("unexpected broker socket directory %q", socketDir)
	}

	var b strings.Builder
	b.WriteString("As root (sudo), jailoc will:\n")
	fmt.Fprintf(&b, "  - stop and remove the service %s and the listener broker %s\n", unitPath, helperPath)
	fmt.Fprintf(&b, "  - remove %s\n", socketDir)
	if hostsChanged {
		fmt.Fprintf(&b, "  - remove the proxy block from %s, leaving the rest of the file as it is\n", d.hostsPath)
	} else {
		fmt.Fprintf(&b, "  - leave %s as it is\n", d.hostsPath)
	}
	if store != nil {
		fmt.Fprintf(&b, "  - remove %s from the system trust store\n", store.CertPath)
	}
	b.WriteString("As you, jailoc will stop the background proxy and remove the trust of the proxy CA that install added")
	if d.goos == "darwin" {
		b.WriteString(" (login keychain, NSS databases).\n")
	} else {
		b.WriteString(" (NSS databases).\n")
	}
	p := d.prompt(yes)
	ok, err := p.confirm(ctx, "Uninstall jailoc proxy host setup?", strings.TrimRight(b.String(), "\n"), true)
	if err != nil {
		return err
	}
	if !ok {
		_, _ = fmt.Fprintln(out, "Aborted.")
		return nil
	}

	if err := stopBackgroundProxy(); err != nil {
		return fmt.Errorf("stop background proxy: %w", err)
	}
	if err := os.Remove(proxyOptionsFile()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove saved proxy options: %w", err)
	}

	stage, err := os.MkdirTemp("", "jailoc-proxy-uninstall-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0o700); err != nil { //nolint:gosec // staging directory must be 0700
		return fmt.Errorf("chmod staging directory: %w", err)
	}
	env := []string{
		"JAILOC_HELPER_DST=" + helperPath,
		"JAILOC_UNIT_DST=" + unitPath,
		"JAILOC_SOCKET_DIR=" + socketDir,
		"JAILOC_SERVICE_LABEL=" + proxyhost.LaunchDaemonLabel,
		"JAILOC_SERVICE_NAME=" + proxyhost.SystemdServiceName,
	}
	if hostsChanged {
		path, sum, err := stageProxyFile(stage, "hosts", hostsNew)
		if err != nil {
			return err
		}
		env = append(env, "JAILOC_HOSTS_SRC="+path, "JAILOC_HOSTS_SHA256="+sum, "JAILOC_HOSTS_DST="+d.hostsPath)
	}
	if store != nil {
		env = append(env, "JAILOC_CA_DST="+store.CertPath)
	}
	if err := d.runPrivileged(ctx, proxyUninstallScript(d.goos, hostsChanged, store), env); err != nil {
		return fmt.Errorf("remove the privileged part of the proxy host setup: %w", err)
	}
	_, _ = fmt.Fprintln(out, "Removed the listener broker service and its binary.")
	if hostsChanged {
		_, _ = fmt.Fprintf(out, "Removed the proxy block from %s.\n", d.hostsPath)
	}

	untrustProxyCA(ctx, d, out, errw, home, prev)
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove install state: %w", err)
	}
	_, _ = fmt.Fprintln(out, "Uninstalled the jailoc proxy host setup.")
	return nil
}

// untrustProxyCA removes the user-level trust of the proxy CA. Failures are
// reported and do not stop the uninstall: the privileged part is gone.
func untrustProxyCA(ctx context.Context, d proxyHostDeps, out, errw io.Writer, home string, prev proxyhost.InstallState) {
	warn := func(format string, a ...any) {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: "+format+"\n", a...)
	}
	if d.goos == "darwin" && prev.MacOSKeychainCert {
		keychain := filepath.Join(home, "Library", "Keychains", proxyKeychainName)
		if err := removeKeychainCA(ctx, d, keychain, prev); err != nil {
			warn("could not remove the proxy CA from the login keychain: %v; delete %q in Keychain Access", err, proxyRootCACommon)
		} else {
			_, _ = fmt.Fprintln(out, "Removed the proxy CA from the macOS login keychain.")
		}
	}
	dbs := slices.Clone(prev.NSSDatabases)
	if found, err := proxyhost.DiscoverNSSDatabases(d.goos, home); err == nil {
		dbs = append(dbs, found...)
	}
	dbs = slices.Compact(slices.Sorted(slices.Values(dbs)))
	if len(dbs) == 0 {
		return
	}
	if _, err := d.lookPath("certutil"); err != nil {
		warn("certutil is not installed; remove %q from these databases by hand: %s", proxyhost.NSSCertNickname, strings.Join(dbs, ", "))
		return
	}
	for _, db := range dbs {
		if !filepath.IsAbs(db) {
			continue
		}
		// An absent entry is the normal case for a database discovered now.
		if err := d.runCommand(ctx, "certutil", "-d", "sql:"+db, "-D", "-n", proxyhost.NSSCertNickname); err == nil {
			_, _ = fmt.Fprintf(out, "Removed the proxy CA from %s.\n", db)
		}
	}
}

// printProxyHostStatus prints the state of the host setup: the listener
// broker, the CA and the host names in the hosts file.
func printProxyHostStatus(ctx context.Context, w io.Writer) {
	d := currentProxyHostDeps
	if ctx == nil {
		ctx = context.Background()
	}
	home, _ := d.homeDir()

	probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := d.probeBroker(probeCtx); err != nil {
		_, _ = fmt.Fprintln(w, "Listener broker: not running (run `jailoc proxy install`)")
	} else {
		_, _ = fmt.Fprintln(w, "Listener broker: running (127.0.0.1:443)")
	}

	_, _ = fmt.Fprintf(w, "CA certificate: %s\n", proxyCAStatus(home))
	_, _ = fmt.Fprintf(w, "Hosts (%s): %s\n", d.hostsPath, proxyHostsStatus(d))
}

func proxyCAStatus(home string) string {
	caPath := proxyCACertPath()
	short := shortenHome(caPath, home)
	data, err := os.ReadFile(caPath) //nolint:gosec // G304: jailoc-owned proxy CA path
	if err != nil {
		return "not created yet (" + short + ")"
	}
	st, err := proxyhost.ReadInstallState(proxyInstallStatePath())
	if err != nil {
		return short + " (install state unreadable: " + err.Error() + ")"
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return short + " (unreadable)"
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return short + " (unreadable)"
	}
	switch {
	case st.CACertSHA256 == "":
		return short + " (not installed by `jailoc proxy install`; trust it in your browser)"
	case st.CACertSHA256 != sha256Hex(block.Bytes):
		return short + " (changed since install; run `jailoc proxy install` to trust the new CA)"
	}
	return short + " (trusted by `jailoc proxy install`)"
}

func proxyHostsStatus(d proxyHostDeps) string {
	cfg, err := loadProxyHostConfig()
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	desired, err := desiredProxyHostNames(cfg)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	if len(desired) == 0 {
		return "nothing needed (no workspace has proxy routes)"
	}
	data, err := os.ReadFile(d.hostsPath) //nolint:gosec // G304: the system hosts file
	if err != nil {
		return "unreadable (" + err.Error() + ")"
	}
	missing, err := proxyhost.CheckHostsContent(data, desired)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	if len(missing) == 0 {
		return fmt.Sprintf("all %d proxy host names present", len(desired))
	}
	return fmt.Sprintf("missing %s; run `jailoc proxy install`", strings.Join(missing, ", "))
}
