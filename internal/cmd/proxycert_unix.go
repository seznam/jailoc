//go:build darwin || linux

package cmd

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/vm"
)

// The proxy CA is a root and an intermediate, both limited to names under
// jailoc.local. The root signs the intermediate once and its key is never
// written to disk; the intermediate signs the leaf certificates.
const (
	proxyCADirName       = "proxy"
	proxyCACert          = "ca.crt"
	proxyIntermediateCrt = "intermediate.crt"
	proxyIntermediateKey = "intermediate.key"
	proxyLeafCert        = "cert.crt"
	proxyLeafKey         = "cert.key"

	proxyCAValidityYears = 10
	proxyLeafValidity    = 90 * 24 * time.Hour
)

// proxyCADir is ~/.local/share/jailoc/proxy, which holds the proxy CA and
// leaf certificate.
func proxyCADir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "share", "jailoc", proxyCADirName)
	}
	return filepath.Join(config.ConfigDir(), proxyCADirName)
}

func proxyCACertPath() string {
	return filepath.Join(proxyCADir(), proxyCACert)
}

// proxyCA is the trusted root and the intermediate that signs leaves.
// rootPEM is the canonical PEM of root: the one certificate that was parsed
// and checked, which is what every trust mechanism must be given.
type proxyCA struct {
	root     *x509.Certificate
	rootPEM  []byte
	inter    *x509.Certificate
	interKey *ecdsa.PrivateKey
	interDER []byte
}

// ensureProxyCert loads or generates the proxy CA and leaf certificate
// covering all DNS-label workspaces in cfg and all routes in targets. It
// returns the leaf and the path of the root certificate to trust.
func ensureProxyCert(cfg *config.Config, targets []proxyTarget) (tls.Certificate, string, error) {
	dir := proxyCADir()
	if err := checkProxyCADirNotShared(dir, cfg, targets); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create proxy CA directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // proxy CA directory must be 0700.
		return tls.Certificate{}, "", fmt.Errorf("chmod proxy CA directory: %w", err)
	}
	var cert tls.Certificate
	err := withProxyCALock(dir, func() error {
		now := time.Now()
		dnsNames, checkHosts := proxyCertSANs(cfg, targets)
		certPath := filepath.Join(dir, proxyLeafCert)
		keyPath := filepath.Join(dir, proxyLeafKey)
		ca, err := loadOrCreateProxyCA(dir, now)
		if err != nil {
			return err
		}
		if existing, err := loadValidProxyLeaf(certPath, keyPath, ca, checkHosts, now); err == nil {
			cert = existing
			return nil
		}
		leaf, err := createProxyLeaf(certPath, keyPath, ca, dnsNames, now)
		if err != nil {
			return err
		}
		cert = leaf
		return nil
	})
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, filepath.Join(dir, proxyCACert), nil
}

// ensureProxyCA loads or generates the proxy CA without issuing a leaf
// certificate. It returns the path of the root certificate, the certificate
// and the canonical PEM of exactly that certificate.
func ensureProxyCA(cfg *config.Config) (string, *x509.Certificate, []byte, error) {
	dir := proxyCADir()
	if err := checkProxyCADirNotShared(dir, cfg, nil); err != nil {
		return "", nil, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, nil, fmt.Errorf("create proxy CA directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // proxy CA directory must be 0700.
		return "", nil, nil, fmt.Errorf("chmod proxy CA directory: %w", err)
	}
	var (
		root    *x509.Certificate
		rootPEM []byte
	)
	err := withProxyCALock(dir, func() error {
		ca, err := loadOrCreateProxyCA(dir, time.Now())
		if err != nil {
			return err
		}
		root, rootPEM = ca.root, ca.rootPEM
		return nil
	})
	if err != nil {
		return "", nil, nil, err
	}
	return filepath.Join(dir, proxyCACert), root, rootPEM, nil
}

func checkProxyCADirNotShared(dir string, cfg *config.Config, targets []proxyTarget) error {
	cleanDir := filepath.Clean(dir)
	realDir := canonicalPath(cleanDir)
	overlaps := func(wsName, hostPath string) error {
		cleanShare := filepath.Clean(hostPath)
		if !filepath.IsAbs(cleanShare) {
			return nil
		}
		// Symlinks are resolved on both sides, so a share that links to the
		// CA directory or one of its parents is caught as well.
		if pathsOverlap(cleanDir, cleanShare) || pathsOverlap(realDir, canonicalPath(cleanShare)) {
			return fmt.Errorf("proxy CA directory %q overlaps shared path %q of workspace %q", cleanDir, cleanShare, wsName)
		}
		return nil
	}
	check := func(wsName, share string) error {
		if share == "" {
			return nil
		}
		hostPath, _, _ := strings.Cut(share, ":")
		if strings.HasPrefix(hostPath, "~/") {
			if home, err := os.UserHomeDir(); err == nil {
				hostPath = filepath.Join(home, hostPath[2:])
			}
		}
		return overlaps(wsName, hostPath)
	}
	// A running VM keeps the shares it booted with until a restart, even
	// when they were since removed from the configuration.
	if err := checkRunningVMShares(overlaps); err != nil {
		return err
	}
	if cfg != nil {
		for _, s := range cfg.Defaults.Mounts {
			if err := check("defaults", s); err != nil {
				return err
			}
		}
		for _, s := range cfg.Defaults.Snapshots {
			if err := check("defaults", s); err != nil {
				return err
			}
		}
		for wsName, ws := range cfg.Workspaces {
			for _, p := range ws.Paths {
				if err := check(wsName, p); err != nil {
					return err
				}
			}
			for _, m := range ws.Mounts {
				if err := check(wsName, m); err != nil {
					return err
				}
			}
			for _, s := range ws.Snapshots {
				if err := check(wsName, s); err != nil {
					return err
				}
			}
		}
	}
	for _, t := range targets {
		for _, p := range t.ws.Paths {
			if err := check(t.ws.Name, p); err != nil {
				return err
			}
		}
		for _, m := range t.ws.Mounts {
			if err := check(t.ws.Name, m); err != nil {
				return err
			}
		}
		for _, s := range t.ws.Snapshots {
			if err := check(t.ws.Name, s); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkRunningVMShares calls overlaps for every share of every workspace VM
// whose supervisor is alive.
func checkRunningVMShares(overlaps func(wsName, hostPath string) error) error {
	entries, err := os.ReadDir(filepath.Join(vmRootDir(), "workspaces"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list workspace VMs: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		stateDir := vmStateDir(e.Name())
		st, err := vm.ReadState(stateDir)
		if err != nil || !processAlive(st.PID) {
			continue // no VM runs from this state directory
		}
		spec, err := vm.ReadSpec(stateDir)
		if err != nil {
			return fmt.Errorf("read VM spec of running workspace %q: %w", e.Name(), err)
		}
		for _, sh := range spec.Shares {
			if err := overlaps(e.Name(), sh.HostPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// canonicalPath resolves symlinks in the longest existing prefix of path and
// appends the rest, so paths that do not exist yet compare by their location.
func canonicalPath(path string) string {
	rest := ""
	for p := path; ; {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return path
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	return pathContains(a, b) || pathContains(b, a)
}

// pathContains reports whether dir is parent or an ancestor of p. Both paths
// are clean and absolute; the root contains every path.
func pathContains(dir, p string) bool {
	sep := string(filepath.Separator)
	if dir == sep {
		return strings.HasPrefix(p, sep)
	}
	return p == dir || strings.HasPrefix(p, dir+sep)
}

func withProxyCALock(dir string, fn func() error) error {
	lockPath := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned proxy CA lock path
	if err != nil {
		return fmt.Errorf("open proxy CA lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }() //nolint:gosec // G115: file descriptors fit in int
			return fn()
		}
		if !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("lock proxy CA directory: %w", err)
		}
	}
}

func proxyCertSANs(cfg *config.Config, targets []proxyTarget) (dnsNames, checkHosts []string) {
	var names []string
	var hosts []string
	if cfg != nil {
		for _, ws := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
			if config.ValidProxyLabel(ws) {
				names = append(names, proxyWorkspaceHost(ws), "*."+ws+"."+proxyDomain)
				hosts = append(hosts, proxyWorkspaceHost(ws), "probe."+ws+"."+proxyDomain)
			}
		}
	}
	for _, t := range targets {
		if config.ValidProxyLabel(t.ws.Name) {
			names = append(names, proxyWorkspaceHost(t.ws.Name), "*."+t.ws.Name+"."+proxyDomain)
			hosts = append(hosts, proxyWorkspaceHost(t.ws.Name))
		}
		for _, r := range t.ws.Proxy {
			h := proxyHost(r.Name, t.ws.Name)
			names = append(names, h)
			hosts = append(hosts, h)
		}
	}
	slices.Sort(names)
	slices.Sort(hosts)
	return slices.Compact(names), slices.Compact(hosts)
}

// loadOrCreateProxyCA reads the root and intermediate in dir, or creates
// both when the root certificate doesn't exist. The root certificate is
// written last, so a directory without it holds nothing anyone trusts and
// is overwritten.
func loadOrCreateProxyCA(dir string, now time.Time) (proxyCA, error) {
	rootPath := filepath.Join(dir, proxyCACert)
	interPath := filepath.Join(dir, proxyIntermediateCrt)
	interKeyPath := filepath.Join(dir, proxyIntermediateKey)
	if _, err := os.Stat(rootPath); !errors.Is(err, os.ErrNotExist) {
		ca, err := readProxyCA(rootPath, interPath, interKeyPath, now)
		if err != nil {
			return proxyCA{}, fmt.Errorf("existing proxy CA in %s is unusable (%w); remove %s to generate a new CA, then trust the new %s", dir, err, dir, rootPath)
		}
		return ca, nil
	}

	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return proxyCA{}, fmt.Errorf("generate proxy root CA key: %w", err)
	}
	rootTmpl, err := proxyCATemplate("jailoc proxy root CA", now, 1)
	if err != nil {
		return proxyCA{}, err
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTmpl, rootTmpl, rootKey.Public(), rootKey)
	if err != nil {
		return proxyCA{}, fmt.Errorf("create proxy root CA certificate: %w", err)
	}
	root, err := x509.ParseCertificate(rootDER)
	if err != nil {
		return proxyCA{}, fmt.Errorf("parse proxy root CA certificate: %w", err)
	}

	interKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return proxyCA{}, fmt.Errorf("generate proxy intermediate CA key: %w", err)
	}
	interTmpl, err := proxyCATemplate("jailoc proxy intermediate CA", now, 0)
	if err != nil {
		return proxyCA{}, err
	}
	interDER, err := x509.CreateCertificate(rand.Reader, interTmpl, root, interKey.Public(), rootKey)
	if err != nil {
		return proxyCA{}, fmt.Errorf("create proxy intermediate CA certificate: %w", err)
	}
	inter, err := x509.ParseCertificate(interDER)
	if err != nil {
		return proxyCA{}, fmt.Errorf("parse proxy intermediate CA certificate: %w", err)
	}

	interKeyDER, err := x509.MarshalPKCS8PrivateKey(interKey)
	if err != nil {
		return proxyCA{}, fmt.Errorf("marshal proxy intermediate CA key: %w", err)
	}
	if err := writePrivateFile(interKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: interKeyDER})); err != nil {
		return proxyCA{}, fmt.Errorf("write proxy intermediate CA key: %w", err)
	}
	if err := writePrivateFile(interPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: interDER})); err != nil {
		return proxyCA{}, fmt.Errorf("write proxy intermediate CA certificate: %w", err)
	}
	rootPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER})
	if err := writePrivateFile(rootPath, rootPEM); err != nil {
		return proxyCA{}, fmt.Errorf("write proxy root CA certificate: %w", err)
	}
	return proxyCA{root: root, rootPEM: rootPEM, inter: inter, interKey: interKey, interDER: interDER}, nil
}

// proxyCATemplate is a CA certificate limited by critical name constraints
// to names under jailoc.local and no IP address. maxPathLen 1 lets the root
// sign the intermediate; 0 lets the intermediate sign only leaves.
func proxyCATemplate(commonName string, now time.Time, maxPathLen int) (*x509.Certificate, error) {
	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(proxyCAValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            maxPathLen,
		MaxPathLenZero:        maxPathLen == 0,

		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         []string{proxyDomain},
		ExcludedIPRanges:            proxyCAExcludedIPRanges(),
	}, nil
}

// parseProxyRootPEM parses data as the root CA file: exactly one certificate
// PEM block and nothing else but surrounding whitespace. Anything more would
// let a tool that reads the file see a certificate that was never checked. It
// returns the certificate and the canonical PEM of it.
func parseProxyRootPEM(data []byte) (*x509.Certificate, []byte, error) {
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) > 0 {
		return nil, nil, errors.New("invalid root CA certificate PEM")
	}
	canonical := pem.EncodeToMemory(block)
	if !bytes.Equal(bytes.TrimSpace(data), bytes.TrimSpace(canonical)) {
		return nil, nil, errors.New("root CA file holds more than one certificate PEM block")
	}
	root, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse root CA: %w", err)
	}
	return root, canonical, nil
}

func readProxyCA(rootPath, interPath, interKeyPath string, now time.Time) (proxyCA, error) {
	data, err := os.ReadFile(rootPath) //nolint:gosec // G304: jailoc-owned proxy CA path
	if err != nil {
		return proxyCA{}, fmt.Errorf("read root CA: %w", err)
	}
	root, rootPEM, err := parseProxyRootPEM(data)
	if err != nil {
		return proxyCA{}, err
	}
	if err := checkProxyCA(root, now); err != nil {
		return proxyCA{}, fmt.Errorf("root CA: %w", err)
	}

	pair, err := tls.LoadX509KeyPair(interPath, interKeyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return proxyCA{}, fmt.Errorf("load intermediate CA: %w", err)
	}
	inter, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return proxyCA{}, fmt.Errorf("parse intermediate CA: %w", err)
	}
	interKey, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return proxyCA{}, errors.New("intermediate CA key is not ECDSA")
	}
	if err := checkProxyCA(inter, now); err != nil {
		return proxyCA{}, fmt.Errorf("intermediate CA: %w", err)
	}
	if err := inter.CheckSignatureFrom(root); err != nil {
		return proxyCA{}, fmt.Errorf("intermediate CA is not signed by the root CA: %w", err)
	}
	return proxyCA{root: root, rootPEM: rootPEM, inter: inter, interKey: interKey, interDER: pair.Certificate[0]}, nil
}

// proxyCAExcludedIPRanges excludes every IPv4 and IPv6 address, so the
// proxy CA can't vouch for an IP address SAN.
func proxyCAExcludedIPRanges() []*net.IPNet {
	return []*net.IPNet{
		{IP: net.IPv4zero.To4(), Mask: net.CIDRMask(0, 32)},
		{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
	}
}

// checkProxyCA accepts a CA certificate that is valid for at least 30 more
// days and is limited by critical name constraints to names under
// jailoc.local and no IP address. A CA without these constraints, such as one
// created by hand, would let its key vouch for any domain.
func checkProxyCA(caCert *x509.Certificate, now time.Time) error {
	if !caCert.IsCA || now.Before(caCert.NotBefore) || !now.Add(30*24*time.Hour).Before(caCert.NotAfter) {
		return errors.New("not a CA, expired or near expiry")
	}
	if !caCert.PermittedDNSDomainsCritical || !slices.Equal(caCert.PermittedDNSDomains, []string{proxyDomain}) {
		return fmt.Errorf("not limited to %s by a critical name constraint", proxyDomain)
	}
	excluded := make([]string, 0, len(caCert.ExcludedIPRanges))
	for _, r := range caCert.ExcludedIPRanges {
		excluded = append(excluded, r.String())
	}
	if !slices.Contains(excluded, "0.0.0.0/0") || !slices.Contains(excluded, "::/0") {
		return errors.New("does not exclude IP addresses by a name constraint")
	}
	return nil
}

// loadValidProxyLeaf loads the leaf in certPath and keyPath if it chains to
// the root through the intermediate, stays valid for 7 more days and covers
// every host in checkHosts.
func loadValidProxyLeaf(certPath, keyPath string, ca proxyCA, checkHosts []string, now time.Time) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, fmt.Errorf("load proxy leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse proxy leaf: %w", err)
	}
	if now.Before(leaf.NotBefore) || !now.Add(7*24*time.Hour).Before(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("proxy leaf is expired or near expiry")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca.root)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(ca.inter)
	for _, h := range checkHosts {
		opts := x509.VerifyOptions{
			DNSName:       h,
			Roots:         roots,
			Intermediates: intermediates,
			CurrentTime:   now,
			KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}
		if _, err := leaf.Verify(opts); err != nil {
			return tls.Certificate{}, fmt.Errorf("proxy leaf does not cover %s: %w", h, err)
		}
	}
	if len(checkHosts) == 0 {
		if err := leaf.CheckSignatureFrom(ca.inter); err != nil {
			return tls.Certificate{}, fmt.Errorf("verify proxy leaf signature: %w", err)
		}
	}
	pair.Leaf = leaf
	return pair, nil
}

func createProxyLeaf(certPath, keyPath string, ca proxyCA, dnsNames []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate proxy leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: proxyDomain},
		DNSNames:     dnsNames,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(proxyLeafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.inter, key.Public(), ca.interKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create proxy leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal proxy leaf key: %w", err)
	}
	chainPEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.interDER})...,
	)
	if err := writePrivateFile(certPath, chainPEM); err != nil {
		return tls.Certificate{}, fmt.Errorf("write proxy leaf certificate: %w", err)
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, fmt.Errorf("write proxy leaf key: %w", err)
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	return n.Add(n, big.NewInt(1)), nil
}

func writePrivateFile(path string, data []byte) error {
	return writeFileAtomically(path, data, 0o600)
}
