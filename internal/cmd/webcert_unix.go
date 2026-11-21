//go:build darwin || linux

package cmd

import (
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
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/seznam/jailoc/internal/config"
)

const (
	ingressDirName  = "ingress"
	ingressCACert   = "ca.crt"
	ingressCAKey    = "ca.key"
	ingressLeafCert = "cert.crt"
	ingressLeafKey  = "cert.key"
)

func ingressDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "jailoc", ingressDirName)
	}
	return filepath.Join(config.ConfigDir(), ingressDirName)
}

func ingressCACertPath() string {
	return filepath.Join(ingressDir(), ingressCACert)
}

// resolveGatewayCert loads the user-supplied --tls-cert/--tls-key pair or,
// when both flags are omitted, ensures the automatic CA and leaf certificate
// in ~/.config/jailoc/ingress cover every route of targets. caPath is set when
// the automatic CA was used.
func resolveGatewayCert(opts gatewayOptions, cfg *config.Config, targets []gatewayTarget) (tls.Certificate, string, error) {
	if opts.certFile != "" || opts.keyFile != "" {
		if opts.certFile == "" || opts.keyFile == "" {
			return tls.Certificate{}, "", errors.New("both --tls-cert and --tls-key must be set, or omit both to use the automatic certificate in ~/.config/jailoc/ingress")
		}
		cert, err := tls.LoadX509KeyPair(opts.certFile, opts.keyFile)
		if err != nil {
			return tls.Certificate{}, "", fmt.Errorf("load TLS certificate: %w", err)
		}
		return cert, "", nil
	}
	return ensureIngressCert(cfg, targets)
}

// ensureIngressCert loads or generates the ingress CA and leaf certificate in
// ~/.config/jailoc/ingress covering all DNS-label workspaces in cfg and all
// routes in targets.
func ensureIngressCert(cfg *config.Config, targets []gatewayTarget) (tls.Certificate, string, error) {
	dir := ingressDir()
	if err := checkIngressDirNotShared(dir, cfg, targets); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create ingress directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // ingress directory must be 0700.
		return tls.Certificate{}, "", fmt.Errorf("chmod ingress directory: %w", err)
	}
	var (
		cert   tls.Certificate
		caPath = filepath.Join(dir, ingressCACert)
	)
	err := withIngressCertLock(dir, func() error {
		now := time.Now()
		dnsNames, checkHosts := ingressSANs(cfg, targets)
		certPath := filepath.Join(dir, ingressLeafCert)
		keyPath := filepath.Join(dir, ingressLeafKey)
		caCertPath := filepath.Join(dir, ingressCACert)
		if caCert, _, err := readIngressCACertOnly(caCertPath, now); err == nil {
			if existing, err := loadValidIngressLeaf(certPath, keyPath, caCert, checkHosts, now); err == nil {
				cert = existing
				return nil
			}
		}
		caCert, caKey, caDER, err := loadOrCreateIngressCA(dir, now)
		if err != nil {
			return err
		}
		if existing, err := loadValidIngressLeaf(certPath, keyPath, caCert, checkHosts, now); err == nil {
			cert = existing
			return nil
		}
		leaf, err := createIngressLeaf(certPath, keyPath, caCert, caKey, caDER, dnsNames, now)
		if err != nil {
			return err
		}
		cert = leaf
		return nil
	})
	if err != nil {
		return tls.Certificate{}, "", err
	}
	return cert, caPath, nil
}

func checkIngressDirNotShared(dir string, cfg *config.Config, targets []gatewayTarget) error {
	cleanDir := filepath.Clean(dir)
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
		cleanShare := filepath.Clean(hostPath)
		if !filepath.IsAbs(cleanShare) {
			return nil
		}
		if pathsOverlap(cleanDir, cleanShare) {
			return fmt.Errorf("ingress CA directory %q overlaps shared path %q of workspace %q", cleanDir, cleanShare, wsName)
		}
		return nil
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

func pathsOverlap(a, b string) bool {
	if a == b {
		return true
	}
	ap := a + string(filepath.Separator)
	bp := b + string(filepath.Separator)
	return strings.HasPrefix(ap, bp) || strings.HasPrefix(bp, ap)
}

func withIngressCertLock(dir string, fn func() error) error {
	lockPath := filepath.Join(dir, ".ingress.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: jailoc-owned ingress lock path
	if err != nil {
		return fmt.Errorf("open ingress lock: %w", err)
	}
	defer func() { _ = f.Close() }()
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX) //nolint:gosec // G115: file descriptors fit in int
		if err == nil {
			defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }() //nolint:gosec // G115: file descriptors fit in int
			return fn()
		}
		if !errors.Is(err, syscall.EINTR) {
			return fmt.Errorf("lock ingress directory: %w", err)
		}
	}
}

func ingressSANs(cfg *config.Config, targets []gatewayTarget) (dnsNames, checkHosts []string) {
	var names []string
	var hosts []string
	if cfg != nil {
		for _, ws := range slices.Sorted(maps.Keys(cfg.Workspaces)) {
			if config.ValidWebLabel(ws) {
				names = append(names, "*."+ws+"."+gatewayDomain)
				hosts = append(hosts, "probe."+ws+"."+gatewayDomain)
			}
		}
	}
	for _, t := range targets {
		if config.ValidWebLabel(t.ws.Name) {
			names = append(names, "*."+t.ws.Name+"."+gatewayDomain)
		}
		for _, r := range t.ws.Proxy {
			h := gatewayHost(r.Name, t.ws.Name)
			names = append(names, h)
			hosts = append(hosts, h)
		}
	}
	slices.Sort(names)
	slices.Sort(hosts)
	return slices.Compact(names), slices.Compact(hosts)
}

func loadOrCreateIngressCA(dir string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPath := filepath.Join(dir, ingressCACert)
	keyPath := filepath.Join(dir, ingressCAKey)
	_, errCert := os.Stat(certPath)
	_, errKey := os.Stat(keyPath)
	if !errors.Is(errCert, os.ErrNotExist) || !errors.Is(errKey, os.ErrNotExist) {
		caCert, caKey, caDER, err := readIngressCA(certPath, keyPath, now)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("existing ingress CA in %s is unusable (%w); remove %s and %s to generate a new CA", dir, err, certPath, keyPath)
		}
		return caCert, caKey, caDER, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate ingress CA key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "jailoc ingress CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, key.Public(), key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create ingress CA certificate: %w", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse ingress CA certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("marshal ingress CA key: %w", err)
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return nil, nil, nil, fmt.Errorf("write ingress CA key: %w", err)
	}
	if err := writePrivateFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return nil, nil, nil, fmt.Errorf("write ingress CA certificate: %w", err)
	}
	return caCert, key, der, nil
}

func readIngressCACertOnly(certPath string, now time.Time) (*x509.Certificate, []byte, error) {
	pemBytes, err := os.ReadFile(certPath) //nolint:gosec // G304: jailoc-owned ingress path
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, nil, errors.New("invalid ingress CA certificate PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if !caCert.IsCA || now.Before(caCert.NotBefore) || !now.Add(30*24*time.Hour).Before(caCert.NotAfter) {
		return nil, nil, errors.New("ingress CA is expired or near expiry")
	}
	return caCert, block.Bytes, nil
}

func readIngressCA(certPath, keyPath string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return nil, nil, nil, fmt.Errorf("load ingress CA: %w", err)
	}
	caCert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse ingress CA: %w", err)
	}
	caKey, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, nil, errors.New("ingress CA key is not ECDSA")
	}
	if !caCert.IsCA || now.Before(caCert.NotBefore) || !now.Add(30*24*time.Hour).Before(caCert.NotAfter) {
		return nil, nil, nil, errors.New("ingress CA is expired or near expiry")
	}
	return caCert, caKey, pair.Certificate[0], nil
}

func loadValidIngressLeaf(certPath, keyPath string, caCert *x509.Certificate, checkHosts []string, now time.Time) (tls.Certificate, error) {
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil || len(pair.Certificate) == 0 {
		return tls.Certificate{}, fmt.Errorf("load ingress leaf: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse ingress leaf: %w", err)
	}
	if err := leaf.CheckSignatureFrom(caCert); err != nil {
		return tls.Certificate{}, fmt.Errorf("verify ingress leaf signature: %w", err)
	}
	if now.Before(leaf.NotBefore) || !now.Add(7*24*time.Hour).Before(leaf.NotAfter) {
		return tls.Certificate{}, errors.New("ingress leaf is expired or near expiry")
	}
	for _, h := range checkHosts {
		if err := leaf.VerifyHostname(h); err != nil {
			return tls.Certificate{}, fmt.Errorf("ingress leaf does not cover %s: %w", h, err)
		}
	}
	pair.Leaf = leaf
	return pair, nil
}

func createIngressLeaf(certPath, keyPath string, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, caDER []byte, dnsNames []string, now time.Time) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate ingress leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: gatewayDomain},
		DNSNames:     dnsNames,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, 90),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, key.Public(), caKey)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create ingress leaf certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal ingress leaf key: %w", err)
	}
	chainPEM := append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})...,
	)
	if err := writePrivateFile(certPath, chainPEM); err != nil {
		return tls.Certificate{}, fmt.Errorf("write ingress leaf certificate: %w", err)
	}
	if err := writePrivateFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return tls.Certificate{}, fmt.Errorf("write ingress leaf key: %w", err)
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
