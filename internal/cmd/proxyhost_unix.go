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
	"regexp"
	"runtime"
	"slices"
	"strconv"
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
	runCommand func(ctx context.Context, name string, args ...string) error
	// runCommandOutput runs a command as the current user and returns its
	// combined output. Tests that only stub runCommand leave it nil.
	runCommandOutput func(ctx context.Context, name string, args ...string) ([]byte, error)
	probeBroker      func(ctx context.Context) error
	// readFile reads an installed system file: the listener broker binary
	// and its service unit.
	readFile func(string) ([]byte, error)
	// checkDestDir verifies that the directories above an install
	// destination are trusted to hold root-owned files.
	checkDestDir func(path string) error
	// runningProxy returns the running background proxy, or nil.
	runningProxy func() *proxyState
	// restartProxy starts the background proxy for workspaces again, in
	// listener broker mode.
	restartProxy func(ctx context.Context, workspaces []string) error
	// interactive reports whether questions can be asked on the terminal.
	interactive func() bool
	// prompt builds the prompter for the confirmation.
	prompt func(yes bool) *promptIO
}

var currentProxyHostDeps = defaultProxyHostDeps

var defaultProxyHostDeps = proxyHostDeps{
	goos:             runtime.GOOS,
	uid:              func() uint32 { return uint32(os.Getuid()) }, //nolint:gosec // UIDs are 32-bit on Linux and Darwin.
	homeDir:          os.UserHomeDir,
	hostsPath:        "/etc/hosts",
	brokerBinary:     defaultProxyBrokerBinary,
	dirExists:        func(p string) bool { fi, err := os.Stat(p); return err == nil && fi.IsDir() },
	lookPath:         exec.LookPath,
	runPrivileged:    runProxyPrivileged,
	runCommand:       runProxyUserCommand,
	runCommandOutput: runProxyUserCommandOutput,
	readFile:         os.ReadFile,
	checkDestDir:     func(p string) error { return proxyhost.CheckTrustedDestDir(p, 0, "/") },
	runningProxy:     runningProxy,
	restartProxy:     restartBrokerProxy,
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

func runProxyUserCommandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // G204: fixed tools with jailoc-derived arguments
	if err != nil {
		return out, fmt.Errorf("%s: %w", name, err)
	}
	return out, nil
}

// restartBrokerProxy starts the background proxy for workspaces again, so
// that it attaches to a listener broker that was restarted.
func restartBrokerProxy(ctx context.Context, workspaces []string) error {
	if err := stopBackgroundProxy(); err != nil {
		return fmt.Errorf("stop background proxy: %w", err)
	}
	return startBackgroundProxy(ctx, workspaces, proxyOptions{listenerBroker: true}, io.Discard)
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

// readProxyHostsFile reads the hosts file; a missing one is empty and exists
// is false.
func readProxyHostsFile(path string) (data []byte, exists bool, err error) {
	data, err = os.ReadFile(path) //nolint:gosec // G304: the system hosts file
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, true, nil
}

const proxyPrivilegedHelpers = `hash_file() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		shasum -a 256 "$1" | cut -d' ' -f1
	fi
}
# check_root_dir_chain DIR: refuse to install below a directory, or any of its
# existing ancestors, that is a symlink, is not owned by root or is writable by
# group or others: whoever controls it could redirect the root-owned install.
check_root_dir_chain() {
	dir="$1"
	while :; do
		if [ -L "$dir" ]; then
			echo "jailoc: $dir is a symlink; refusing to install into an untrusted directory" >&2
			exit 1
		fi
		if [ -e "$dir" ]; then
			if [ ! -d "$dir" ] || [ ! -O "$dir" ]; then
				echo "jailoc: $dir is not a root-owned directory; refusing to install" >&2
				exit 1
			fi
			case "$(ls -ld "$dir" | awk '{print $1}')" in
				d????w????*|d???????w?*)
					echo "jailoc: $dir is writable by group or others; refusing to install" >&2
					exit 1
					;;
			esac
		fi
		[ "$dir" = "/" ] && break
		dir="$(dirname "$dir")"
	done
}
# install_file MODE SRC DST SHA256 [PREV_SHA256]: copy SRC next to DST as root,
# check that the copy is the file jailoc hashed, and only then move it into
# place, so a file replaced after jailoc prepared it is never installed as
# root. With PREV_SHA256 DST must still be what jailoc planned against ("missing"
# for no file), so a file of another user is never overwritten.
install_file() {
	check_root_dir_chain "$(dirname "$3")"
	[ -d "$(dirname "$3")" ] || install -d -m 0755 -o 0 -g 0 "$(dirname "$3")"
	check_root_dir_chain "$(dirname "$3")"
	install -m "$1" -o 0 -g 0 "$2" "$3.jailoc-new"
	if [ "$(hash_file "$3.jailoc-new")" != "$4" ]; then
		rm -f "$3.jailoc-new"
		echo "jailoc: $2 changed while it was being installed; nothing was installed" >&2
		exit 1
	fi
	if [ -n "${5:-}" ]; then
		if [ "$5" = "missing" ]; then
			if [ -e "$3" ] || [ -L "$3" ]; then
				rm -f "$3.jailoc-new"
				echo "jailoc: $3 appeared while waiting for confirmation; leaving it untouched" >&2
				exit 1
			fi
		elif [ -L "$3" ] || [ ! -f "$3" ] || [ "$(hash_file "$3")" != "$5" ]; then
			rm -f "$3.jailoc-new"
			echo "jailoc: $3 is not the certificate jailoc expected; leaving it untouched" >&2
			exit 1
		fi
	fi
	mv -f "$3.jailoc-new" "$3"
}
# remove_ca_file DST SHA256: remove DST, unless it exists and is not the
# certificate jailoc installed there: it then belongs to another user.
remove_ca_file() {
	if [ -L "$1" ] || { [ -e "$1" ] && { [ ! -f "$1" ] || [ "$(hash_file "$1")" != "$2" ]; }; }; then
		echo "jailoc: $1 is not the certificate jailoc installed; leaving it untouched" >&2
		exit 1
	fi
	rm -f "$1"
}
# write_hosts SRC SHA256 DST: replace the content of DST (keeping a symlink
# in place) with the verified SRC, unless DST is no longer the file jailoc
# planned the change against (JAILOC_HOSTS_PREV_SHA256).
write_hosts() {
	tmp="$(mktemp)"
	cat "$1" > "$tmp"
	if [ "$(hash_file "$tmp")" != "$2" ]; then
		rm -f "$tmp"
		echo "jailoc: $1 changed while it was being installed; hosts file untouched" >&2
		exit 1
	fi
	if [ -n "${JAILOC_HOSTS_PREV_SHA256:-}" ]; then
		if [ "$JAILOC_HOSTS_PREV_SHA256" = "missing" ]; then
			if [ -e "$3" ]; then
				rm -f "$tmp"
				echo "jailoc: $3 was created while waiting for confirmation; hosts file untouched" >&2
				exit 1
			fi
		elif [ ! -f "$3" ] || [ "$(hash_file "$3")" != "$JAILOC_HOSTS_PREV_SHA256" ]; then
			rm -f "$tmp"
			echo "jailoc: $3 changed while waiting for confirmation; hosts file untouched" >&2
			exit 1
		fi
	fi
	cat "$tmp" > "$3"
	rm -f "$tmp"
}
`

// proxyInstallScript is the privileged part of `jailoc proxy install`.
// withService installs and restarts the listener broker service; an
// unchanged, running one is left alone so that active proxy sessions survive
// a repeated install.
func proxyInstallScript(goos string, withService, withHosts bool, store *proxyhost.LinuxCAStore) string {
	var b strings.Builder
	b.WriteString(proxyPrivilegedHelpers)
	if withService {
		b.WriteString(`install_file 0755 "$JAILOC_HELPER_SRC" "$JAILOC_HELPER_DST" "$JAILOC_HELPER_SHA256"` + "\n")
		b.WriteString(`install_file 0644 "$JAILOC_UNIT_SRC" "$JAILOC_UNIT_DST" "$JAILOC_UNIT_SHA256"` + "\n")
	}
	if withHosts {
		b.WriteString(`write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"` + "\n")
	}
	if store != nil {
		b.WriteString(`install_file 0644 "$JAILOC_CA_SRC" "$JAILOC_CA_DST" "$JAILOC_CA_SHA256" "$JAILOC_CA_PREV_SHA256"` + "\n")
		b.WriteString(shellJoin(store.UpdateCmd) + "\n")
	}
	if !withService {
		return b.String()
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
// withService stops and removes the listener broker service and binary; it is
// false when the installed service belongs to another user.
func proxyUninstallScript(goos string, withService, withHosts bool, store *proxyhost.LinuxCAStore) string {
	var b strings.Builder
	b.WriteString(proxyPrivilegedHelpers)
	if withService {
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
	}
	if withHosts {
		b.WriteString(`write_hosts "$JAILOC_HOSTS_SRC" "$JAILOC_HOSTS_SHA256" "$JAILOC_HOSTS_DST"` + "\n")
	}
	if store != nil {
		b.WriteString(`remove_ca_file "$JAILOC_CA_DST" "$JAILOC_CA_EXPECTED_SHA256"` + "\n")
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
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // G304,G703: staging file inside a jailoc-created temporary directory
		return "", "", fmt.Errorf("stage %s: %w", name, err)
	}
	return path, sha256Hex(data), nil
}

// withProxyHostLock serializes jailoc host setup runs: install.json and
// /etc/hosts are read, confirmed and written back, and two runs interleaving
// would each plan against the state the other is about to replace.
func withProxyHostLock(fn func() error) error {
	dir := proxyCADir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create proxy CA directory: %w", err)
	}
	return withProxyCALock(dir, fn)
}

// fileHasSHA256 reports whether the file at path exists and has SHA-256 sum.
func (d proxyHostDeps) fileHasSHA256(path, sum string) bool {
	data, err := d.readFile(path)
	return err == nil && sha256Hex(data) == sum
}

// fileGone reports whether path no longer exists.
func (d proxyHostDeps) fileGone(path string) bool {
	_, err := d.readFile(path)
	return errors.Is(err, os.ErrNotExist)
}

// hostsPrevSHA256 is the value of JAILOC_HOSTS_PREV_SHA256: what the
// privileged script must still find in the hosts file.
func hostsPrevSHA256(hostsCur []byte, exists bool) string {
	if !exists {
		return "missing"
	}
	return sha256Hex(hostsCur)
}

// serviceOwnedByUID reports whether the service unit at unitPath is
// installed, and whether it runs the listener broker for uid: either it is
// byte for byte what jailoc would install for uid, or its command line names
// uid.
func serviceOwnedByUID(d proxyHostDeps, unitPath, helperPath string, uid uint32) (present, owned bool) {
	onDisk, err := d.readFile(unitPath)
	if err != nil {
		// An unreadable unit is as good as an absent one: it cannot be shown
		// to belong to another user either.
		return false, false
	}
	var expected []byte
	var uidArg *regexp.Regexp
	uidStr := strconv.FormatUint(uint64(uid), 10)
	if d.goos == "darwin" {
		expected, err = proxyhost.RenderLaunchDaemonPlist(helperPath, uid)
		uidArg = regexp.MustCompile(`<string>--uid</string>\s*<string>` + uidStr + `</string>`)
	} else {
		expected, err = proxyhost.RenderSystemdUnit(helperPath, uid)
		uidArg = regexp.MustCompile(`(?m)^ExecStart=.* --uid ` + uidStr + `\s*$`)
	}
	if err != nil {
		return true, false
	}
	return true, bytes.Equal(bytes.TrimSpace(onDisk), bytes.TrimSpace(expected)) || uidArg.Match(onDisk)
}

// proxyInstallInputs is what runProxyInstall prepares before it takes the
// host setup lock.
type proxyInstallInputs struct {
	home     string
	caPath   string
	caSHA256 string
	rootPEM  []byte
	cfg      *config.Config
}

// proxyInstallOutcome is what the locked part of an install leaves for the
// steps that must run without the lock.
type proxyInstallOutcome struct {
	// installed is false when the user declined.
	installed bool
	trustErr  error
	// restart is the background proxy running in listener broker mode before
	// the broker service was restarted; it must attach again.
	restart *proxyState
}

func runProxyInstall(cmd *cobra.Command, yes bool) error {
	d := currentProxyHostDeps
	ctx := proxyHostContext(cmd)
	out, errw := cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := checkProxyHostInvocation(d, "install", yes); err != nil {
		return err
	}
	home, err := d.homeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	cfg, err := loadProxyHostConfig()
	if err != nil {
		return err
	}
	// ensureProxyCA takes the host setup lock itself, so it runs before it.
	caPath, root, rootPEM, err := ensureProxyCA(cfg)
	if err != nil {
		return err
	}
	caSHA256, _ := caFingerprints(root.Raw)
	in := proxyInstallInputs{home: home, caPath: caPath, caSHA256: caSHA256, rootPEM: rootPEM, cfg: cfg}

	var res proxyInstallOutcome
	if err := withProxyHostLock(func() error {
		var err error
		res, err = installProxyHost(ctx, d, yes, out, errw, in)
		return err
	}); err != nil {
		return err
	}
	if !res.installed {
		return nil
	}

	// The proxy's own start takes the host setup lock for the CA, so these
	// steps run after it is released.
	if st := d.runningProxy(); st != nil && !st.ListenerBroker {
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
		if res.restart != nil {
			if err := d.restartProxy(ctx, slices.Sorted(maps.Keys(res.restart.Workspaces))); err != nil {
				_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: could not attach the background proxy to the restarted listener broker (%v); run `jailoc proxy stop`, then start a workspace again\n", err)
			} else {
				_, _ = fmt.Fprintln(out, "Attached the background proxy to the restarted listener broker.")
			}
		}
	}
	return res.trustErr
}

// installProxyHost is the part of an install that runs under the host setup
// lock.
func installProxyHost(ctx context.Context, d proxyHostDeps, yes bool, out, errw io.Writer, in proxyInstallInputs) (proxyInstallOutcome, error) {
	helperPath := proxyhost.HelperBinaryPath(d.goos)
	unitPath := proxyhost.ServiceUnitPath(d.goos)
	uid := d.uid()

	statePath := proxyInstallStatePath()
	prev, err := proxyhost.ReadInstallState(statePath)
	if err != nil {
		return proxyInstallOutcome{}, err
	}
	desired, err := desiredProxyHostNames(in.cfg)
	if err != nil {
		return proxyInstallOutcome{}, err
	}
	hostsCur, hostsExist, err := readProxyHostsFile(d.hostsPath)
	if err != nil {
		return proxyInstallOutcome{}, err
	}
	plan, err := proxyhost.PlanHosts(hostsCur, proxyhost.Ledger{Owned: prev.HostsLedger}, desired)
	if err != nil {
		return proxyInstallOutcome{}, fmt.Errorf("plan %s: %w (run `jailoc proxy uninstall`, or remove both the `# BEGIN/END jailoc proxy hosts` block in %s and %s to reconcile them, then run install again)", d.hostsPath, err, d.hostsPath, statePath)
	}
	hostsChanged := !bytes.Equal(plan.Content, hostsCur)

	var unit []byte
	if d.goos == "darwin" {
		unit, err = proxyhost.RenderLaunchDaemonPlist(helperPath, uid)
	} else {
		unit, err = proxyhost.RenderSystemdUnit(helperPath, uid)
	}
	if err != nil {
		return proxyInstallOutcome{}, err
	}

	var store *proxyhost.LinuxCAStore
	if d.goos == "linux" {
		if s, ok := proxyhost.DetectLinuxCAStore(d.dirExists, d.lookPath); ok {
			store = &s
		}
	}

	// The service is shared by every user of the host: replacing the one that
	// runs for another user would disconnect that user's proxy.
	if unitPresent, unitOwned := serviceOwnedByUID(d, unitPath, helperPath, uid); unitPresent && !unitOwned {
		return proxyInstallOutcome{}, fmt.Errorf("the installed listener broker service %s belongs to another user; refusing to replace it", unitPath)
	}

	caPrevSHA256 := ""
	if store != nil {
		if caPrevSHA256, err = systemCAPrevSHA256(d, store.CertPath, prev, in.rootPEM); err != nil {
			return proxyInstallOutcome{}, err
		}
	}

	brokerSrc, cleanup, err := d.brokerBinary()
	if err != nil {
		return proxyInstallOutcome{}, err
	}
	defer cleanup()
	brokerData, err := os.ReadFile(brokerSrc) //nolint:gosec // G304: path from the broker binary seam
	if err != nil {
		return proxyInstallOutcome{}, fmt.Errorf("read listener broker binary: %w", err)
	}

	// Installing the service again restarts the broker and drops the
	// connections of active proxy sessions, so an installed, identical and
	// running one is left alone.
	needService := !d.fileHasSHA256(helperPath, sha256Hex(brokerData)) ||
		!d.fileHasSHA256(unitPath, sha256Hex(unit)) ||
		d.probeBroker(ctx) != nil
	if needService {
		for _, dst := range []string{helperPath, unitPath} {
			if err := d.checkDestDir(dst); err != nil {
				return proxyInstallOutcome{}, fmt.Errorf("check where %s is installed: %w", dst, err)
			}
		}
	}

	stage, err := os.MkdirTemp("", "jailoc-proxy-install-")
	if err != nil {
		return proxyInstallOutcome{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0o700); err != nil { //nolint:gosec // staging directory must be 0700
		return proxyInstallOutcome{}, fmt.Errorf("chmod staging directory: %w", err)
	}
	var env []string
	stageEnv := func(name, srcKey, shaKey string, data []byte) error {
		p, sum, err := stageProxyFile(stage, name, data)
		if err != nil {
			return err
		}
		env = append(env, srcKey+"="+p, shaKey+"="+sum)
		return nil
	}
	if needService {
		env = append(env,
			"JAILOC_HELPER_DST="+helperPath,
			"JAILOC_UNIT_DST="+unitPath,
			"JAILOC_SERVICE_LABEL="+proxyhost.LaunchDaemonLabel,
			"JAILOC_SERVICE_NAME="+proxyhost.SystemdServiceName,
		)
		if err := stageEnv("helper", "JAILOC_HELPER_SRC", "JAILOC_HELPER_SHA256", brokerData); err != nil {
			return proxyInstallOutcome{}, err
		}
		if err := stageEnv("service-unit", "JAILOC_UNIT_SRC", "JAILOC_UNIT_SHA256", unit); err != nil {
			return proxyInstallOutcome{}, err
		}
	}
	if hostsChanged {
		if err := stageEnv("hosts", "JAILOC_HOSTS_SRC", "JAILOC_HOSTS_SHA256", plan.Content); err != nil {
			return proxyInstallOutcome{}, err
		}
		env = append(env, "JAILOC_HOSTS_DST="+d.hostsPath, "JAILOC_HOSTS_PREV_SHA256="+hostsPrevSHA256(hostsCur, hostsExist))
	}
	if store != nil {
		if err := stageEnv("ca.crt", "JAILOC_CA_SRC", "JAILOC_CA_SHA256", in.rootPEM); err != nil {
			return proxyInstallOutcome{}, err
		}
		env = append(env, "JAILOC_CA_DST="+store.CertPath, "JAILOC_CA_PREV_SHA256="+caPrevSHA256)
	}

	nssDBs, nssErr := proxyhost.DiscoverNSSDatabases(d.goos, in.home)
	_, certutilErr := d.lookPath("certutil")

	summary := proxyInstallSummary(d, in.home, helperPath, unitPath, needService, hostsChanged, len(desired), plan, store, nssDBs, certutilErr == nil)
	p := d.prompt(yes)
	ok, err := p.confirm(ctx, "Install host support for jailoc proxy?", summary, true)
	if err != nil {
		return proxyInstallOutcome{}, err
	}
	if !ok {
		_, _ = fmt.Fprintln(out, "Aborted.")
		return proxyInstallOutcome{}, nil
	}

	var restart *proxyState
	if needService {
		if st := d.runningProxy(); st != nil && st.ListenerBroker {
			restart = st
		}
	}
	if needService || hostsChanged || store != nil {
		if err := d.runPrivileged(ctx, proxyInstallScript(d.goos, needService, hostsChanged, store), env); err != nil {
			// The script ran step by step: record what it finished, so that
			// uninstall can still revert it.
			partial := prev
			if hostsChanged {
				if curNow, _, rerr := readProxyHostsFile(d.hostsPath); rerr == nil && bytes.Equal(curNow, plan.Content) {
					partial.HostsLedger = plan.Ledger.Owned
				}
			}
			if needService && d.fileHasSHA256(helperPath, sha256Hex(brokerData)) {
				partial.HelperPath = helperPath
			}
			if needService && d.fileHasSHA256(unitPath, sha256Hex(unit)) {
				partial.ServiceUnitPath = unitPath
			}
			if store != nil && d.fileHasSHA256(store.CertPath, sha256Hex(in.rootPEM)) {
				partial.SystemCACertPath = store.CertPath
				partial.SystemCACertSHA256 = sha256Hex(in.rootPEM)
				partial.SystemCAUpdateCmd = store.UpdateCmd
				partial.SystemCARemoveCmd = store.RemoveCmd
			}
			err = fmt.Errorf("install the privileged part of the proxy host setup: %w", err)
			if partial.HasAnything() {
				if werr := proxyhost.WriteInstallState(statePath, partial); werr != nil {
					err = errors.Join(err, werr)
				}
			}
			return proxyInstallOutcome{}, err
		}
	}

	// Nothing removed the system CA that was installed earlier when no store
	// was detected now, so it stays recorded. The CA that rotated out and the
	// ones already pending stay recorded as pending until they are retired.
	pendingKeychain, pendingNSS := pendingCAIdentities(prev, in.caSHA256, in.rootPEM)
	state := proxyhost.InstallState{
		HostsLedger:           plan.Ledger.Owned,
		CACertSHA256:          in.caSHA256,
		CACertPEM:             in.rootPEM,
		SystemCACertPath:      prev.SystemCACertPath,
		SystemCACertSHA256:    prev.SystemCACertSHA256,
		SystemCAUpdateCmd:     prev.SystemCAUpdateCmd,
		SystemCARemoveCmd:     prev.SystemCARemoveCmd,
		MacOSKeychainCert:     prev.MacOSKeychainCert,
		NSSDatabases:          prev.NSSDatabases,
		HelperPath:            helperPath,
		ServiceUnitPath:       unitPath,
		PendingKeychainSHA1s:  pendingKeychain,
		PendingNSSCertSHA256s: pendingNSS,
	}
	if store != nil {
		state.SystemCACertPath = store.CertPath
		state.SystemCACertSHA256 = sha256Hex(in.rootPEM)
		state.SystemCAUpdateCmd = store.UpdateCmd
		state.SystemCARemoveCmd = store.RemoveCmd
	}
	if err := proxyhost.WriteInstallState(statePath, state); err != nil {
		return proxyInstallOutcome{}, err
	}
	if err := writeProxySavedOptions(proxyOptions{listenerBroker: true}); err != nil {
		return proxyInstallOutcome{}, fmt.Errorf("save proxy options: %w", err)
	}
	if needService {
		_, _ = fmt.Fprintf(out, "Installed %s and its service (%s).\n", helperPath, unitPath)
	} else {
		_, _ = fmt.Fprintf(out, "%s and its service (%s) are up to date.\n", helperPath, unitPath)
	}
	if hostsChanged {
		_, _ = fmt.Fprintf(out, "Updated %s.\n", d.hostsPath)
	}

	trustErr := trustProxyCA(ctx, d, out, errw, in.home, in.caPath, in.caSHA256, prev, &state, nssDBs, nssErr, store, certutilErr == nil)
	if err := proxyhost.WriteInstallState(statePath, state); err != nil {
		return proxyInstallOutcome{}, errors.Join(trustErr, err)
	}
	return proxyInstallOutcome{installed: true, trustErr: trustErr, restart: restart}, nil
}

// systemCAPrevSHA256 is the value of JAILOC_CA_PREV_SHA256: what the
// privileged script must still find at the system CA path. A certificate that
// is neither the one being installed nor one jailoc recorded belongs to
// another user and is not replaced.
func systemCAPrevSHA256(d proxyHostDeps, certPath string, prev proxyhost.InstallState, rootPEM []byte) (string, error) {
	existing, err := d.readFile(certPath)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", fmt.Errorf("read existing system CA %s: %w", certPath, err)
	}
	sum := sha256Hex(existing)
	owned := sum == sha256Hex(rootPEM) ||
		(prev.SystemCACertSHA256 != "" && sum == prev.SystemCACertSHA256) ||
		(len(prev.CACertPEM) > 0 && sum == sha256Hex(prev.CACertPEM))
	if !owned {
		return "", fmt.Errorf("system CA path %s already holds a certificate not installed by this user; refusing to overwrite it", certPath)
	}
	return sum, nil
}

// pendingCAIdentities are the SHA-1s in the macOS keychain and the SHA-256s
// in NSS databases of earlier proxy CAs that are still to be retired: the
// ones already pending and the one recorded in prev when the CA rotated.
func pendingCAIdentities(prev proxyhost.InstallState, caSHA256 string, rootPEM []byte) (keychainSHA1s, nssSHA256s []string) {
	keychainSHA1s = slices.Clone(prev.PendingKeychainSHA1s)
	nssSHA256s = slices.Clone(prev.PendingNSSCertSHA256s)
	if prev.CACertSHA256 != "" && prev.CACertSHA256 != caSHA256 {
		if s1 := pemSHA1(prev.CACertPEM); prev.MacOSKeychainCert && s1 != "" && s1 != pemSHA1(rootPEM) {
			keychainSHA1s = append(keychainSHA1s, s1)
		}
		if len(prev.NSSDatabases) > 0 {
			nssSHA256s = append(nssSHA256s, prev.CACertSHA256)
		}
	}
	return slices.Compact(slices.Sorted(slices.Values(keychainSHA1s))), slices.Compact(slices.Sorted(slices.Values(nssSHA256s)))
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

func proxyInstallSummary(d proxyHostDeps, home, helperPath, unitPath string, needService, hostsChanged bool, desiredCount int, plan proxyhost.Plan, store *proxyhost.LinuxCAStore, nssDBs []string, haveCertutil bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "As root (sudo), jailoc will:\n")
	if needService {
		fmt.Fprintf(&b, "  - install the listener broker %s (127.0.0.1:443 and [::1]:443 for uid %d)\n", helperPath, d.uid())
		fmt.Fprintf(&b, "  - install and start its service %s\n", unitPath)
	} else {
		fmt.Fprintf(&b, "  - leave the running listener broker %s and its service %s as they are (already up to date)\n", helperPath, unitPath)
	}
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

// inspectNSSCert lists the jailoc proxy CA entry of the NSS database spec:
// whether there is one, and the SHA-256 of each certificate certutil prints.
// Without runCommandOutput only the presence is known and sha256s is empty.
func inspectNSSCert(ctx context.Context, d proxyHostDeps, spec string) (present bool, sha256s []string, err error) {
	if d.runCommandOutput == nil {
		return d.runCommand(ctx, "certutil", "-d", spec, "-L", "-n", proxyhost.NSSCertNickname) == nil, nil, nil
	}
	out, err := d.runCommandOutput(ctx, "certutil", "-d", spec, "-L", "-n", proxyhost.NSSCertNickname, "-a")
	if err != nil {
		if bytes.Contains(bytes.ToLower(out), []byte("could not find")) {
			return false, nil, nil
		}
		return false, nil, fmt.Errorf("list %q in %s: %w", proxyhost.NSSCertNickname, spec, err)
	}
	for rest := out; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type == "CERTIFICATE" {
			sha256s = append(sha256s, sha256Hex(block.Bytes))
		}
	}
	if len(sha256s) == 0 {
		return false, nil, fmt.Errorf("list %q in %s: no certificate in the output of certutil", proxyhost.NSSCertNickname, spec)
	}
	return true, sha256s, nil
}

// deleteKeychainCert deletes the certificate with SHA-1 sha1 from the
// keychain with its trust settings, or the one named proxyRootCACommon when
// sha1 is empty. A certificate that is not there is deleted.
func deleteKeychainCert(ctx context.Context, d proxyHostDeps, keychain, sha1 string) error {
	args := []string{"delete-certificate", "-c", proxyRootCACommon, "-t", keychain}
	if sha1 != "" {
		args = []string{"delete-certificate", "-Z", sha1, "-t", keychain}
	}
	err := d.runCommand(ctx, "security", args...)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "could not be found in the keychain") {
		return nil
	}
	return err
}

// retireKeychainCerts deletes the certificates of earlier jailoc proxy CAs
// from the keychain: the one recorded in prev when the CA rotated, and the
// ones an earlier run could not delete. It returns the SHA-1s that are still
// there, so that a later install or uninstall retries them.
func retireKeychainCerts(ctx context.Context, d proxyHostDeps, warn func(string, ...any), keychain string, prev proxyhost.InstallState, caSHA256, currentSHA1 string) []string {
	stale := slices.Clone(prev.PendingKeychainSHA1s)
	if prev.MacOSKeychainCert && prev.CACertSHA256 != "" && prev.CACertSHA256 != caSHA256 {
		if s1 := pemSHA1(prev.CACertPEM); s1 != "" {
			stale = append(stale, s1)
		} else if err := deleteKeychainCert(ctx, d, keychain, ""); err != nil {
			warn("could not remove the previous proxy CA from the login keychain: %v", err)
		}
	}
	var pending []string
	for _, s1 := range slices.Compact(slices.Sorted(slices.Values(stale))) {
		if s1 == currentSHA1 {
			continue
		}
		if err := deleteKeychainCert(ctx, d, keychain, s1); err != nil {
			warn("could not remove the previous proxy CA %s from the login keychain: %v; the next `jailoc proxy install` or `uninstall` tries again", s1, err)
			pending = append(pending, s1)
		}
	}
	return pending
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
	switch {
	case d.goos == "darwin":
		keychain := filepath.Join(home, "Library", "Keychains", proxyKeychainName)
		state.PendingKeychainSHA1s = retireKeychainCerts(ctx, d, warn, keychain, prev, caSHA256, pemSHA1(state.CACertPEM))
		if err := d.runCommand(ctx, "security", "add-trusted-cert", "-r", "trustRoot", "-k", keychain, caPath); err != nil {
			result = fmt.Errorf("trust the proxy CA in the login keychain: %w (trust %s by hand in Keychain Access)", err, caPath)
		} else {
			state.MacOSKeychainCert = true
			_, _ = fmt.Fprintln(out, "Trusted the proxy CA in the macOS login keychain.")
		}
	case store != nil:
		_, _ = fmt.Fprintf(out, "Trusted the proxy CA in the system store (%s).\n", store.CertPath)
	default:
		warn("no supported Linux system trust store found; trust %s by hand (for example copy it to your distribution's CA directory and run its update tool)", caPath)
	}

	if nssErr != nil {
		warn("could not look for Firefox and Chrome certificate databases: %v", nssErr)
	}
	// The entry named like ours is replaced only when it is one of ours: the
	// current CA, the recorded one or one an earlier rotation left behind.
	owned := map[string]bool{caSHA256: true}
	var stale []string
	for _, s := range append(slices.Clone(prev.PendingNSSCertSHA256s), prev.CACertSHA256) {
		if s != "" && s != caSHA256 {
			owned[s] = true
			stale = append(stale, s)
		}
	}
	// A deleted Firefox profile has no database left to keep track of.
	added := slices.DeleteFunc(slices.Clone(prev.NSSDatabases), func(db string) bool { return !d.dirExists(db) })
	refreshed := map[string]bool{}
	var manual, foreign []string
	for _, db := range nssDBs {
		if !haveCertutil {
			manual = append(manual, db)
			continue
		}
		spec := "sql:" + db
		present, fingerprints, err := inspectNSSCert(ctx, d, spec)
		if err != nil {
			warn("could not look at the certificates of %s: %v", db, err)
			manual = append(manual, db)
			continue
		}
		if present && len(fingerprints) > 0 && !slices.ContainsFunc(fingerprints, func(s string) bool { return s != caSHA256 }) {
			// certutil -L shows the certificate, not its trust flags, which
			// may be missing or have been removed in the browser.
			if err := d.runCommand(ctx, "certutil", "-d", spec, "-M", "-t", "C,,", "-n", proxyhost.NSSCertNickname); err != nil {
				warn("could not set the trust of the proxy CA in %s: %v", db, err)
				manual = append(manual, db)
				continue
			}
			added = append(added, db)
			refreshed[db] = true
			continue
		}
		if slices.ContainsFunc(fingerprints, func(s string) bool { return !owned[s] }) {
			warn("refusing to replace existing certificate %q in %s because its fingerprint does not match jailoc's recorded CA", proxyhost.NSSCertNickname, db)
			manual = append(manual, db)
			foreign = append(foreign, db)
			continue
		}
		if present {
			if err := d.runCommand(ctx, "certutil", "-d", spec, "-D", "-n", proxyhost.NSSCertNickname); err != nil {
				warn("could not remove the previous proxy CA from %s: %v", db, err)
				manual = append(manual, db)
				continue
			}
		}
		if err := d.runCommand(ctx, "certutil", "-d", spec, "-A", "-t", "C,,", "-n", proxyhost.NSSCertNickname, "-i", caPath); err != nil {
			warn("could not add the proxy CA to %s: %v", db, err)
			manual = append(manual, db)
			continue
		}
		added = append(added, db)
		refreshed[db] = true
	}
	added = slices.DeleteFunc(added, func(db string) bool { return slices.Contains(foreign, db) })
	state.NSSDatabases = slices.Compact(slices.Sorted(slices.Values(added)))
	// An old CA may still be in a database that was not refreshed.
	state.PendingNSSCertSHA256s = nil
	if len(manual) > 0 || slices.ContainsFunc(state.NSSDatabases, func(db string) bool { return !refreshed[db] }) {
		state.PendingNSSCertSHA256s = slices.Compact(slices.Sorted(slices.Values(stale)))
	}
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
	return withProxyHostLock(func() error {
		return uninstallProxyHost(ctx, d, yes, out, errw)
	})
}

// uninstallProxyHost is an uninstall under the host setup lock.
func uninstallProxyHost(ctx context.Context, d proxyHostDeps, yes bool, out, errw io.Writer) error {
	warn := func(format string, a ...any) {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: "+format+"\n", a...)
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

	// The service is shared by every user of the host: only the one it runs
	// for may remove it.
	unitPresent, unitOwned := serviceOwnedByUID(d, unitPath, helperPath, uid)
	removeService := (prev.HelperPath != "" || prev.ServiceUnitPath != "" || unitOwned) && (!unitPresent || unitOwned)
	if !prev.HasAnything() && !removeService {
		if unitPresent && !unitOwned {
			return fmt.Errorf("the installed listener broker service %s belongs to another user; refusing to uninstall it", unitPath)
		}
		_, _ = fmt.Fprintln(out, "Proxy host setup is not installed.")
		return nil
	}

	hostsCur, hostsExist, err := readProxyHostsFile(d.hostsPath)
	if err != nil {
		return err
	}
	var hostsNew []byte
	hostsChanged := false
	if len(prev.HostsLedger) > 0 {
		plan, err := proxyhost.PlanHosts(hostsCur, proxyhost.Ledger{Owned: prev.HostsLedger}, nil)
		switch {
		case err == nil:
			hostsNew = plan.Content
			hostsChanged = !bytes.Equal(hostsNew, hostsCur)
		case !bytes.Contains(hostsCur, prev.HostsLedger) &&
			!bytes.Contains(hostsCur, []byte("# BEGIN jailoc proxy hosts")) &&
			!bytes.Contains(hostsCur, []byte("# END jailoc proxy hosts")):
			// An earlier, partial uninstall already removed the block.
			prev.HostsLedger = nil
		default:
			return fmt.Errorf("revert %s: %w (remove the jailoc block by hand, then run uninstall again)", d.hostsPath, err)
		}
	}
	var store *proxyhost.LinuxCAStore
	if d.goos == "linux" {
		if s, ok := trustedSystemCAStore(prev); ok {
			store = s
		} else if prev.SystemCACertPath != "" {
			warn("ignoring the system CA path %q recorded in %s: it is not a path jailoc installs to", prev.SystemCACertPath, statePath)
		}
	}
	caSHA256 := ""
	if store != nil {
		if caSHA256, store, err = verifySystemCAOwnership(d, store, prev, warn); err != nil {
			return err
		}
	}
	socketDir := filepath.Dir(proxyhost.BrokerSocketPath(uid))
	if !strings.HasPrefix(socketDir, proxySocketDirRoot) {
		return fmt.Errorf("unexpected broker socket directory %q", socketDir)
	}
	runRoot := removeService || hostsChanged || store != nil

	var b strings.Builder
	if runRoot {
		b.WriteString("As root (sudo), jailoc will:\n")
	}
	if removeService {
		fmt.Fprintf(&b, "  - stop and remove the service %s and the listener broker %s\n", unitPath, helperPath)
		fmt.Fprintf(&b, "  - remove %s\n", socketDir)
	}
	if hostsChanged {
		fmt.Fprintf(&b, "  - remove the proxy block from %s, leaving the rest of the file as it is\n", d.hostsPath)
	}
	if store != nil {
		fmt.Fprintf(&b, "  - remove %s from the system trust store\n", store.CertPath)
	}
	if unitPresent && !unitOwned {
		fmt.Fprintf(&b, "The service %s belongs to another user and stays in place.\n", unitPath)
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

	if runRoot {
		if err := runPrivilegedUninstall(ctx, d, proxyUninstallPlan{
			removeService: removeService,
			socketDir:     socketDir,
			hostsChanged:  hostsChanged,
			hostsCur:      hostsCur,
			hostsExist:    hostsExist,
			hostsNew:      hostsNew,
			store:         store,
			caSHA256:      caSHA256,
		}); err != nil {
			// Keep what the script did not finish for the next uninstall.
			if hostsChanged {
				if cur, _, rerr := readProxyHostsFile(d.hostsPath); rerr == nil && bytes.Equal(cur, hostsNew) {
					prev.HostsLedger = nil
				}
			}
			if removeService && d.fileGone(helperPath) {
				prev.HelperPath = ""
			}
			if removeService && d.fileGone(unitPath) {
				prev.ServiceUnitPath = ""
			}
			err = fmt.Errorf("remove the privileged part of the proxy host setup: %w", err)
			if prev.HasAnything() {
				if werr := proxyhost.WriteInstallState(statePath, prev); werr != nil {
					err = errors.Join(err, werr)
				}
			}
			return err
		}
	}
	if removeService {
		_, _ = fmt.Fprintln(out, "Removed the listener broker service and its binary.")
	} else if unitPresent {
		warn("the listener broker service %s belongs to another user and was left in place", unitPath)
	}
	if hostsChanged {
		_, _ = fmt.Fprintf(out, "Removed the proxy block from %s.\n", d.hostsPath)
	}
	prev.HostsLedger = nil
	prev.HelperPath, prev.ServiceUnitPath = "", ""
	prev.SystemCACertPath, prev.SystemCACertSHA256, prev.SystemCAUpdateCmd, prev.SystemCARemoveCmd = "", "", nil, nil

	remaining, untrustErr := untrustProxyCA(ctx, d, out, errw, home, prev)
	if remaining.HasAnything() {
		if err := proxyhost.WriteInstallState(statePath, remaining); err != nil {
			return errors.Join(untrustErr, err)
		}
		return fmt.Errorf("the privileged part of the proxy host setup is removed, but not all trust of the proxy CA: %w (run `jailoc proxy uninstall` again)", untrustErr)
	}
	if err := os.Remove(statePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove install state: %w", err)
	}
	_, _ = fmt.Fprintln(out, "Uninstalled the jailoc proxy host setup.")
	return nil
}

// verifySystemCAOwnership returns the SHA-256 of the system CA jailoc
// installed, and store unchanged when the file at the system CA path is it or
// is gone. A certificate that is another one, or whose identity jailoc did not
// record, belongs to someone else and is left alone: store is then nil.
func verifySystemCAOwnership(d proxyHostDeps, store *proxyhost.LinuxCAStore, prev proxyhost.InstallState, warn func(string, ...any)) (string, *proxyhost.LinuxCAStore, error) {
	expected := prev.SystemCACertSHA256
	if expected == "" && len(prev.CACertPEM) > 0 {
		expected = sha256Hex(prev.CACertPEM)
	}
	onDisk, err := d.readFile(store.CertPath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Already gone; the script still refreshes the system bundle.
		return expected, store, nil
	case err != nil:
		return "", nil, fmt.Errorf("read system CA %s: %w", store.CertPath, err)
	case expected == "" || sha256Hex(onDisk) != expected:
		warn("not removing %s because its certificate does not match jailoc's recorded CA", store.CertPath)
		return "", nil, nil
	}
	return expected, store, nil
}

// proxyUninstallPlan is what the privileged part of an uninstall does.
type proxyUninstallPlan struct {
	removeService bool
	socketDir     string
	hostsChanged  bool
	// hostsCur and hostsExist are the hosts file the plan was made against,
	// hostsNew what it becomes.
	hostsCur   []byte
	hostsExist bool
	hostsNew   []byte
	store      *proxyhost.LinuxCAStore
	// caSHA256 is the SHA-256 of the certificate the script may remove from
	// the system store.
	caSHA256 string
}

// runPrivilegedUninstall runs the privileged part of an uninstall.
func runPrivilegedUninstall(ctx context.Context, d proxyHostDeps, plan proxyUninstallPlan) error {
	stage, err := os.MkdirTemp("", "jailoc-proxy-uninstall-")
	if err != nil {
		return fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := os.Chmod(stage, 0o700); err != nil { //nolint:gosec // staging directory must be 0700
		return fmt.Errorf("chmod staging directory: %w", err)
	}
	var env []string
	if plan.removeService {
		env = append(env,
			"JAILOC_HELPER_DST="+proxyhost.HelperBinaryPath(d.goos),
			"JAILOC_UNIT_DST="+proxyhost.ServiceUnitPath(d.goos),
			"JAILOC_SOCKET_DIR="+plan.socketDir,
			"JAILOC_SERVICE_LABEL="+proxyhost.LaunchDaemonLabel,
			"JAILOC_SERVICE_NAME="+proxyhost.SystemdServiceName,
		)
	}
	if plan.hostsChanged {
		path, sum, err := stageProxyFile(stage, "hosts", plan.hostsNew)
		if err != nil {
			return err
		}
		env = append(env, "JAILOC_HOSTS_SRC="+path, "JAILOC_HOSTS_SHA256="+sum, "JAILOC_HOSTS_DST="+d.hostsPath, "JAILOC_HOSTS_PREV_SHA256="+hostsPrevSHA256(plan.hostsCur, plan.hostsExist))
	}
	if plan.store != nil {
		env = append(env, "JAILOC_CA_DST="+plan.store.CertPath, "JAILOC_CA_EXPECTED_SHA256="+plan.caSHA256)
	}
	return d.runPrivileged(ctx, proxyUninstallScript(d.goos, plan.removeService, plan.hostsChanged, plan.store), env)
}

// untrustProxyCA removes the user-level trust of the proxy CA that prev
// records. It returns the trust it could not remove, which the install state
// must keep for a retry, and the reason.
func untrustProxyCA(ctx context.Context, d proxyHostDeps, out, errw io.Writer, home string, prev proxyhost.InstallState) (remaining proxyhost.InstallState, err error) {
	warn := func(format string, a ...any) {
		_, _ = color.New(color.FgYellow).Fprintf(errw, "WARNING: "+format+"\n", a...)
	}
	keepCA := func() {
		remaining.CACertSHA256 = prev.CACertSHA256
		remaining.CACertPEM = prev.CACertPEM
	}
	if d.goos == "darwin" && (prev.MacOSKeychainCert || len(prev.PendingKeychainSHA1s) > 0) {
		keychain := filepath.Join(home, "Library", "Keychains", proxyKeychainName)
		if prev.MacOSKeychainCert {
			if derr := deleteKeychainCert(ctx, d, keychain, pemSHA1(prev.CACertPEM)); derr != nil {
				warn("could not remove the proxy CA from the login keychain: %v; delete %q in Keychain Access", derr, proxyRootCACommon)
				remaining.MacOSKeychainCert = true
				keepCA()
				err = errors.Join(err, fmt.Errorf("remove the proxy CA from the login keychain: %w", derr))
			} else {
				_, _ = fmt.Fprintln(out, "Removed the proxy CA from the macOS login keychain.")
			}
		}
		for _, s1 := range prev.PendingKeychainSHA1s {
			if derr := deleteKeychainCert(ctx, d, keychain, s1); derr != nil {
				warn("could not remove the previous proxy CA %s from the login keychain: %v", s1, derr)
				remaining.PendingKeychainSHA1s = append(remaining.PendingKeychainSHA1s, s1)
				keepCA()
				err = errors.Join(err, fmt.Errorf("remove the previous proxy CA %s from the login keychain: %w", s1, derr))
			}
		}
	}

	var dbs []string
	for _, db := range slices.Compact(slices.Sorted(slices.Values(prev.NSSDatabases))) {
		// A deleted Firefox profile has no certificate left to remove.
		if filepath.IsAbs(db) && d.dirExists(db) {
			dbs = append(dbs, db)
		}
	}
	if len(dbs) == 0 {
		return remaining, err
	}
	keepNSS := func(failed ...string) {
		remaining.NSSDatabases = append(remaining.NSSDatabases, failed...)
		remaining.PendingNSSCertSHA256s = prev.PendingNSSCertSHA256s
		keepCA()
	}
	if _, lerr := d.lookPath("certutil"); lerr != nil {
		warn("certutil is not installed; remove %q from these databases by hand: %s", proxyhost.NSSCertNickname, strings.Join(dbs, ", "))
		keepNSS(dbs...)
		return remaining, errors.Join(err, errors.New("certutil is not installed"))
	}
	ours := map[string]bool{prev.CACertSHA256: true}
	for _, s := range prev.PendingNSSCertSHA256s {
		ours[s] = true
	}
	for _, db := range dbs {
		spec := "sql:" + db
		if d.runCommandOutput != nil {
			present, fingerprints, ierr := inspectNSSCert(ctx, d, spec)
			switch {
			case ierr != nil:
				warn("could not look at the certificates of %s: %v", db, ierr)
				keepNSS(db)
				err = errors.Join(err, ierr)
				continue
			case !present:
				continue
			case slices.ContainsFunc(fingerprints, func(s string) bool { return !ours[s] }):
				warn("not removing certificate %q from %s because its fingerprint does not match jailoc's recorded CA", proxyhost.NSSCertNickname, db)
				continue
			}
		}
		derr := d.runCommand(ctx, "certutil", "-d", spec, "-D", "-n", proxyhost.NSSCertNickname)
		if derr == nil {
			_, _ = fmt.Fprintf(out, "Removed the proxy CA from %s.\n", db)
			continue
		}
		// Another jailoc run may have removed it already.
		if stillThere, _, ierr := inspectNSSCert(ctx, d, spec); ierr == nil && !stillThere {
			continue
		}
		warn("could not remove the proxy CA from %s: %v", db, derr)
		keepNSS(db)
		err = errors.Join(err, fmt.Errorf("remove the proxy CA from %s: %w", db, derr))
	}
	return remaining, err
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
	var stores []string
	if st.MacOSKeychainCert {
		stores = append(stores, "login keychain")
	}
	if st.SystemCACertPath != "" {
		stores = append(stores, "system store")
	}
	if len(st.NSSDatabases) > 0 {
		stores = append(stores, fmt.Sprintf("%d NSS database(s)", len(st.NSSDatabases)))
	}
	switch {
	case st.CACertSHA256 == "" || len(stores) == 0:
		return short + " (not trusted in any store by `jailoc proxy install`; trust it in your browser or run `jailoc proxy install`)"
	case st.CACertSHA256 != sha256Hex(block.Bytes):
		return short + " (changed since install; run `jailoc proxy install` to trust the new CA)"
	}
	return fmt.Sprintf("%s (trusted by `jailoc proxy install`: %s)", short, strings.Join(stores, ", "))
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
