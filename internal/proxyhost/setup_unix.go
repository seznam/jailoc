//go:build darwin || linux

package proxyhost

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode"
)

const (
	// LaunchDaemonLabel is the launchd label of the privileged listener
	// broker on macOS.
	LaunchDaemonLabel = "cz.seznam.jailoc.listener-broker"
	// SystemdServiceName is the systemd unit of the privileged listener
	// broker on Linux.
	SystemdServiceName = "jailoc-listener-broker.service"
	// NSSCertNickname is the nickname of the proxy CA in NSS databases.
	NSSCertNickname = "jailoc proxy CA"
)

const (
	debianCADir    = "/usr/local/share/ca-certificates"
	fedoraCADir    = "/etc/pki/ca-trust/source/anchors"
	archCADir      = "/etc/ca-certificates/trust-source/anchors"
	linuxCAFile    = "jailoc-proxy-ca.crt"
	macHelperPath  = "/Library/PrivilegedHelperTools/jailoc-listener-broker"
	linuxHelper    = "/usr/local/libexec/jailoc-listener-broker"
	macServiceUnit = "/Library/LaunchDaemons/" + LaunchDaemonLabel + ".plist"
	linuxService   = "/etc/systemd/system/" + SystemdServiceName
)

// HelperBinaryPath returns where the privileged listener broker binary is
// installed on goos, or "" for an unsupported goos.
func HelperBinaryPath(goos string) string {
	switch goos {
	case "darwin":
		return macHelperPath
	case "linux":
		return linuxHelper
	default:
		return ""
	}
}

// ServiceUnitPath returns where the service definition of the privileged
// listener broker is installed on goos, or "" for an unsupported goos.
func ServiceUnitPath(goos string) string {
	switch goos {
	case "darwin":
		return macServiceUnit
	case "linux":
		return linuxService
	default:
		return ""
	}
}

// validateServiceArgs checks the inputs shared by the service renderers.
// Control characters cannot appear in either format.
func validateServiceArgs(helperPath string, uid uint32) error {
	if !filepath.IsAbs(helperPath) {
		return fmt.Errorf("helper path %q is not absolute", helperPath)
	}
	if strings.IndexFunc(helperPath, unicode.IsControl) >= 0 {
		return fmt.Errorf("helper path %q contains a control character", helperPath)
	}
	if uid == 0 {
		return errors.New("uid must be greater than zero")
	}
	return nil
}

// RenderLaunchDaemonPlist renders the macOS LaunchDaemon that runs the
// listener broker at boot for uid and restarts it when it exits.
func RenderLaunchDaemonPlist(helperPath string, uid uint32) ([]byte, error) {
	if err := validateServiceArgs(helperPath, uid); err != nil {
		return nil, fmt.Errorf("render launch daemon plist: %w", err)
	}
	var escaped bytes.Buffer
	if err := xml.EscapeText(&escaped, []byte(helperPath)); err != nil {
		return nil, fmt.Errorf("render launch daemon plist: escape helper path: %w", err)
	}
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0">` + "\n")
	b.WriteString("<dict>\n")
	b.WriteString("\t<key>Label</key>\n")
	b.WriteString("\t<string>" + LaunchDaemonLabel + "</string>\n")
	b.WriteString("\t<key>ProgramArguments</key>\n")
	b.WriteString("\t<array>\n")
	b.WriteString("\t\t<string>" + escaped.String() + "</string>\n")
	b.WriteString("\t\t<string>--uid</string>\n")
	b.WriteString("\t\t<string>" + strconv.FormatUint(uint64(uid), 10) + "</string>\n")
	b.WriteString("\t</array>\n")
	b.WriteString("\t<key>RunAtLoad</key>\n")
	b.WriteString("\t<true/>\n")
	b.WriteString("\t<key>KeepAlive</key>\n")
	b.WriteString("\t<true/>\n")
	b.WriteString("</dict>\n")
	b.WriteString("</plist>\n")
	return b.Bytes(), nil
}

// RenderSystemdUnit renders the systemd unit that runs the listener broker
// for uid and restarts it when it exits.
func RenderSystemdUnit(helperPath string, uid uint32) ([]byte, error) {
	if err := validateServiceArgs(helperPath, uid); err != nil {
		return nil, fmt.Errorf("render systemd unit: %w", err)
	}
	// ExecStart splits on whitespace and expands quotes, escapes and
	// specifiers, so a path using any of them would run a different file.
	if strings.ContainsFunc(helperPath, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(`"'\%$;`, r)
	}) {
		return nil, fmt.Errorf("render systemd unit: helper path %q contains a character systemd would interpret", helperPath)
	}
	var b strings.Builder
	b.WriteString("[Unit]\n")
	b.WriteString("Description=jailoc proxy listener broker\n")
	b.WriteString("\n")
	b.WriteString("[Service]\n")
	b.WriteString("Type=simple\n")
	b.WriteString("ExecStart=" + helperPath + " --uid " + strconv.FormatUint(uint64(uid), 10) + "\n")
	b.WriteString("Restart=always\n")
	b.WriteString("RestartSec=1\n")
	b.WriteString("\n")
	b.WriteString("[Install]\n")
	b.WriteString("WantedBy=multi-user.target\n")
	return []byte(b.String()), nil
}

// InstallState records what host setup changed so that uninstall can revert
// exactly those changes and a CA rotation can replace the old certificate.
type InstallState struct {
	HostsLedger      []byte `json:"hosts_ledger,omitempty"`
	CACertSHA256     string `json:"ca_cert_sha256,omitempty"`
	CACertPEM        []byte `json:"ca_cert_pem,omitempty"`
	SystemCACertPath string `json:"system_ca_cert_path,omitempty"`
	// SystemCACertSHA256 is the SHA-256 of the exact file jailoc installed at
	// SystemCACertPath: the file is shared by every user of the host, so
	// replacing or removing it requires finding this content there.
	SystemCACertSHA256 string   `json:"system_ca_cert_sha256,omitempty"`
	SystemCAUpdateCmd  []string `json:"system_ca_update_cmd,omitempty"`
	SystemCARemoveCmd  []string `json:"system_ca_remove_cmd,omitempty"`
	MacOSKeychainCert  bool     `json:"macos_keychain_cert,omitempty"`
	NSSDatabases       []string `json:"nss_databases,omitempty"`
	HelperPath         string   `json:"helper_path,omitempty"`
	ServiceUnitPath    string   `json:"service_unit_path,omitempty"`
	// PendingKeychainSHA1s are the SHA-1s of earlier proxy CAs that a CA
	// rotation could not delete from the macOS keychain yet.
	PendingKeychainSHA1s []string `json:"pending_keychain_sha1s,omitempty"`
	// PendingNSSCertSHA256s are the SHA-256s of earlier proxy CAs that may
	// still be in NSS databases under NSSCertNickname.
	PendingNSSCertSHA256s []string `json:"pending_nss_cert_sha256s,omitempty"`
	// PendingNSSDatabases are the NSS databases that may still hold an
	// earlier proxy CA after a rotation; NSSDatabases lists only those
	// where the current CA is installed.
	PendingNSSDatabases []string `json:"pending_nss_databases,omitempty"`
}

// HasAnything reports whether st records any change to revert.
func (st InstallState) HasAnything() bool {
	return len(st.HostsLedger) > 0 ||
		st.CACertSHA256 != "" ||
		len(st.CACertPEM) > 0 ||
		st.SystemCACertPath != "" ||
		st.SystemCACertSHA256 != "" ||
		len(st.SystemCAUpdateCmd) > 0 ||
		len(st.SystemCARemoveCmd) > 0 ||
		st.MacOSKeychainCert ||
		len(st.NSSDatabases) > 0 ||
		st.HelperPath != "" ||
		st.ServiceUnitPath != "" ||
		len(st.PendingKeychainSHA1s) > 0 ||
		len(st.PendingNSSCertSHA256s) > 0 ||
		len(st.PendingNSSDatabases) > 0
}

// ReadInstallState reads the install state at path. A missing file yields
// the zero state and no error.
func ReadInstallState(path string) (InstallState, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: caller-supplied install state path
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return InstallState{}, nil
		}
		return InstallState{}, fmt.Errorf("read install state: %w", err)
	}
	var st InstallState
	if err := json.Unmarshal(data, &st); err != nil {
		return InstallState{}, fmt.Errorf("parse install state %s: %w", path, err)
	}
	return st, nil
}

// WriteInstallState atomically replaces the install state at path with st,
// mode 0600, creating the parent directory with mode 0700 when needed.
func WriteInstallState(path string, st InstallState) (err error) {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("encode install state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create install state directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary install state: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, removeIfExists(tmp.Name()))
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return errors.Join(fmt.Errorf("chmod temporary install state: %w", err), closeFile(tmp))
	}
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write temporary install state: %w", err), closeFile(tmp))
	}
	if err := tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temporary install state: %w", err), closeFile(tmp))
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary install state: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replace install state: %w", err)
	}
	return nil
}

// CheckTrustedDestDir verifies that every existing directory above
// destFilePath, up to and including trustTop, is a directory (not a symlink)
// owned by trustedOwner and not writable by group or others. Directories that
// do not exist yet are skipped: they are created by the installer, which
// checks them again.
func CheckTrustedDestDir(destFilePath string, trustedOwner uint32, trustTop string) error {
	dir := filepath.Dir(filepath.Clean(destFilePath))
	top := filepath.Clean(trustTop)
	if !filepath.IsAbs(dir) || !filepath.IsAbs(top) {
		return fmt.Errorf("check destination %q: path is not absolute", destFilePath)
	}
	for {
		if err := brokerCmdCheckNode(dir, trustedOwner, true); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("untrusted destination directory: %w", err)
		}
		parent := filepath.Dir(dir)
		if dir == top || parent == dir {
			return nil
		}
		dir = parent
	}
}

func closeFile(f *os.File) error {
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", f.Name(), err)
	}
	return nil
}

func removeIfExists(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}

// LinuxCAStore describes how to add and remove a CA certificate in a Linux
// distribution's system trust store.
type LinuxCAStore struct {
	// CertPath is where the CA certificate is written.
	CertPath string
	// UpdateCmd rebuilds the trust store after CertPath is added.
	UpdateCmd []string
	// RemoveCmd rebuilds the trust store after CertPath is removed.
	RemoveCmd []string
}

// DetectLinuxCAStore finds the system trust store layout of the host using
// the injected directory and PATH lookups. It tries the Debian/Ubuntu,
// Fedora/RHEL and Arch layouts in that order.
func DetectLinuxCAStore(dirExists func(string) bool, lookPath func(string) (string, error)) (LinuxCAStore, bool) {
	if dirExists == nil || lookPath == nil {
		return LinuxCAStore{}, false
	}
	candidates := []struct {
		dir   string
		tool  string
		store LinuxCAStore
	}{
		{debianCADir, "update-ca-certificates", LinuxCAStore{
			CertPath:  debianCADir + "/" + linuxCAFile,
			UpdateCmd: []string{"update-ca-certificates"},
			RemoveCmd: []string{"update-ca-certificates", "--fresh"},
		}},
		{fedoraCADir, "update-ca-trust", LinuxCAStore{
			CertPath:  fedoraCADir + "/" + linuxCAFile,
			UpdateCmd: []string{"update-ca-trust"},
			RemoveCmd: []string{"update-ca-trust"},
		}},
		{archCADir, "trust", LinuxCAStore{
			CertPath:  archCADir + "/" + linuxCAFile,
			UpdateCmd: []string{"trust", "extract-compat"},
			RemoveCmd: []string{"trust", "extract-compat"},
		}},
	}
	for _, c := range candidates {
		if !dirExists(c.dir) {
			continue
		}
		if _, err := lookPath(c.tool); err != nil {
			continue
		}
		return c.store, true
	}
	return LinuxCAStore{}, false
}

// DiscoverNSSDatabases returns the sorted, deduplicated NSS database
// directories under home that hold a certificate store: ~/.pki/nssdb on
// Linux (Chrome and Chromium) and the Firefox profiles of goos. A directory
// qualifies when it contains cert9.db; ~/.pki/nssdb also qualifies with the
// legacy cert8.db.
func DiscoverNSSDatabases(goos, home string) ([]string, error) {
	var profileRoots []string
	switch goos {
	case "darwin":
		profileRoots = []string{filepath.Join(home, "Library", "Application Support", "Firefox", "Profiles")}
	case "linux":
		profileRoots = []string{
			filepath.Join(home, ".mozilla", "firefox"),
			filepath.Join(home, "snap", "firefox", "common", ".mozilla", "firefox"),
			filepath.Join(home, ".var", "app", "org.mozilla.firefox", ".mozilla", "firefox"),
		}
	default:
		return nil, fmt.Errorf("discover NSS databases: unsupported OS %q", goos)
	}
	if !filepath.IsAbs(home) {
		return nil, fmt.Errorf("discover NSS databases: home %q is not absolute", home)
	}

	found := map[string]struct{}{}
	if goos == "linux" {
		chrome := filepath.Join(home, ".pki", "nssdb")
		if hasAnyFile(chrome, "cert9.db", "cert8.db") {
			found[chrome] = struct{}{}
		}
	}
	for _, root := range profileRoots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				continue
			}
			return nil, fmt.Errorf("discover NSS databases: read %s: %w", root, err)
		}
		for _, e := range entries {
			dir := filepath.Join(root, e.Name())
			if hasAnyFile(dir, "cert9.db") {
				found[dir] = struct{}{}
			}
		}
	}

	dbs := make([]string, 0, len(found))
	for d := range found {
		dbs = append(dbs, d)
	}
	slices.Sort(dbs)
	return dbs, nil
}

// hasAnyFile reports whether dir (following symlinks) holds a regular file
// named any of names.
func hasAnyFile(dir string, names ...string) bool {
	for _, n := range names {
		fi, err := os.Stat(filepath.Join(dir, n))
		if err == nil && fi.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// ProbeBroker reports whether a listener broker owned by brokerUID answers on
// the health endpoint next to socketPath. It never connects to socketPath, so
// it cannot take the gateway slot, whether or not another gateway is
// attached. addr is validated like the one of AttachBroker. A broker that
// predates the health endpoint is refused, not attached to: restart or
// reinstall it.
func ProbeBroker(ctx context.Context, socketPath string, brokerUID uint32, addr *net.TCPAddr) error {
	if _, err := brokerListenAddr(addr); err != nil {
		return fmt.Errorf("probe listener broker: %w", err)
	}
	if err := checkBrokerHealth(ctx, socketPath, brokerUID); err != nil {
		return fmt.Errorf("probe listener broker: %w", err)
	}
	return nil
}
