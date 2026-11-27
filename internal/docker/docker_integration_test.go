//go:build integration

package docker

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	archive "github.com/moby/go-archive"
	dockerclient "github.com/moby/moby/client"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/workspace"
)

func skipWithoutDocker(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Skip("Docker client not available: ", err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.Ping(ctx, dockerclient.PingOptions{}); err != nil {
		t.Skip("Docker daemon not reachable: ", err)
	}
}

func TestResolveBaseImageNilCfg(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tag, err := ResolveBaseImage(context.Background(), nil, "v0.0.0-test")
	if err == nil {
		if tag != "jailoc-base:embedded" {
			t.Fatalf("unexpected tag on nil cfg without error: got %q, want %q", tag, "jailoc-base:embedded")
		}
		return
	}

	if tag != "" {
		t.Fatalf("expected empty tag on error, got %q", tag)
	}
}

func TestResolveBaseImageEmbeddedFallbackWhenNoImageConfig(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	cfg := &config.Config{}
	tag, err := ResolveBaseImage(context.Background(), cfg, "v0.0.0-test")
	if err == nil {
		if tag != "jailoc-base:embedded" {
			t.Fatalf("unexpected tag without error: got %q, want %q", tag, "jailoc-base:embedded")
		}
		return
	}

	if strings.Contains(err.Error(), "load dockerfile") {
		t.Fatalf("unexpected dockerfile load error for empty image config: %v", err)
	}
	if tag != "" {
		t.Fatalf("expected empty tag on error, got %q", tag)
	}
}

func TestBuildOverlayImageDefaultBuildContext(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tmpDir := t.TempDir()
	wsDockerfile := filepath.Join(tmpDir, "overlay.Dockerfile")
	if err := os.WriteFile(wsDockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:       "ws-default-ctx",
		Dockerfile: wsDockerfile,
	})
	if err == nil {
		t.Fatal("expected docker build error (daemon/build stage), got nil")
	}
	if strings.Contains(err.Error(), "determine build context") {
		t.Fatalf("unexpected build context resolution error: %v", err)
	}
	if strings.Contains(err.Error(), "write temporary workspace Dockerfile") {
		t.Fatalf("unexpected Dockerfile temp write error: %v", err)
	}
}

func TestBuildEmbeddedImage(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	const tag = "jailoc-base:integration-test"
	if err := buildEmbeddedImage(ctx, cli, tag, ""); err != nil {
		t.Fatalf("buildEmbeddedImage: %v", err)
	}

	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), tag, dockerclient.ImageRemoveOptions{Force: true})
	})

	inspect, err := cli.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatalf("inspect built image %q: %v", tag, err)
	}
	if inspect.ID == "" {
		t.Fatal("built image has empty ID")
	}
}

func TestBuildOverlayImageImplicitBaseArg(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	tmpDir := t.TempDir()
	wsDockerfile := filepath.Join(tmpDir, "overlay.Dockerfile")
	// Dockerfile uses ${BASE} without declaring ARG BASE — jailoc must prepend it automatically.
	if err := os.WriteFile(wsDockerfile, []byte("FROM ${BASE}\nRUN echo hello\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:       "ws-implicit-base",
		Dockerfile: wsDockerfile,
	})
	if err == nil {
		t.Fatal("expected docker build error (image pull), got nil")
	}
	if strings.Contains(err.Error(), "should not be blank") {
		t.Fatalf("BASE arg was not injected: %v", err)
	}
}

func TestBuildOverlayImageExplicitBuildContext(t *testing.T) {
	t.Parallel()
	skipWithoutDocker(t)

	dockerfileDir := t.TempDir()
	buildContextDir := t.TempDir()
	wsDockerfile := filepath.Join(dockerfileDir, "overlay.Dockerfile")
	if err := os.WriteFile(wsDockerfile, []byte("ARG BASE\nFROM ${BASE}\n"), 0o600); err != nil {
		t.Fatalf("write workspace dockerfile: %v", err)
	}

	_, err := BuildOverlayImage(context.Background(), "registry.example.com/base:v1", workspace.Resolved{
		Name:         "ws-explicit-ctx",
		Dockerfile:   wsDockerfile,
		BuildContext: buildContextDir,
	})
	if err == nil {
		t.Fatal("expected docker build error (daemon/build stage), got nil")
	}
	if strings.Contains(err.Error(), "determine build context") {
		t.Fatalf("unexpected build context resolution error: %v", err)
	}
	if strings.Contains(err.Error(), "write temporary workspace Dockerfile") {
		t.Fatalf("unexpected Dockerfile temp write error: %v", err)
	}
}

func TestEnsureImageReplacesForeignPlatform(t *testing.T) {
	skipWithoutDocker(t)
	ctx := context.Background()
	const ref = "busybox:1.36.1"
	foreign := "linux/amd64"
	if runtime.GOARCH == "amd64" {
		foreign = "linux/arm64"
	}
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.ImageRemove(ctx, ref, dockerclient.ImageRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		t.Fatalf("remove %s: %v", ref, err)
	}
	if err := pullImage(ctx, cli, ref, foreign); err != nil {
		t.Fatalf("pull %s for %s: %v", ref, foreign, err)
	}
	if info, err := cli.ImageInspect(ctx, ref); err != nil || imagePlatform(info) == "linux/"+runtime.GOARCH {
		t.Fatalf("precondition: %s is cached for the host platform: %v", ref, err)
	}
	if err := EnsureImage(ctx, ref); err != nil {
		t.Fatalf("EnsureImage: %v", err)
	}
	info, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := imagePlatform(info), "linux/"+runtime.GOARCH; got != want {
		t.Fatalf("image platform = %s, want %s", got, want)
	}
}

const (
	caTestDebianBase  = "debian:bookworm-slim"
	caTestBusyboxBase = "busybox:1.36.1"
	caTestBaseTag     = "jailoc-ca-test-base:integration"
)

// caTestOverlayDockerfile verifies the CA trust steps jailoc injects after
// FROM. The checks run in a regular RUN step, i.e. after the trust step, so
// they see what any workspace Dockerfile sees.
const caTestOverlayDockerfile = `FROM ${BASE}
COPY expected-ca.pem present.line absent.line server-present.pem server-absent.pem /tmp/jailoc-test/
RUN set -eu; \
    t=/tmp/jailoc-test; \
    certdir=/usr/local/share/ca-certificates; \
    cert=$certdir/jailoc-build-ca-001.crt; \
    bundle=/etc/ssl/certs/ca-certificates.crt; \
    cmp "$t/expected-ca.pem" "$cert"; \
    test "$(ls $certdir/jailoc-build-ca-*.crt | wc -l)" -eq 1; \
    grep -qFf "$t/present.line" "$bundle"; \
    if grep -qFf "$t/absent.line" "$bundle"; then echo "foreign CA is trusted" >&2; exit 1; fi; \
    if grep -qFf "$t/absent.line" "$cert"; then echo "foreign CA is in the forwarded cert" >&2; exit 1; fi; \
    test "$(grep -c 'BEGIN CERTIFICATE' "$bundle")" -ge 11; \
test "$UV_SYSTEM_CERTS" = true; \
test "$NODE_USE_SYSTEM_CA" = 1; \
test "$SSL_CERT_FILE" = "$bundle"; \
    openssl verify -CAfile "$bundle" /opt/jailoc-base-root.crt >/dev/null; \
    openssl verify -CAfile "$bundle" "$t/server-present.pem" >/dev/null; \
    if openssl verify -CAfile "$bundle" "$t/server-absent.pem" >/dev/null 2>&1; then echo "foreign leaf verified" >&2; exit 1; fi; \
if [ -e /run/secrets/jailoc-ca ]; then echo "CA secret leaked into a RUN step" >&2; exit 1; fi
RUN --mount=type=cache,target=/tmp/jailoc-cache,uid=${CACHE_UID} test "$(stat -c %u /tmp/jailoc-cache)" = 65534
ENV SSL_CERT_FILE=/custom/ca.pem
RUN test "$SSL_CERT_FILE" = /custom/ca.pem && test "$REQUESTS_CA_BUNDLE" = /etc/ssl/certs/ca-certificates.crt && test ! -e /run/secrets/jailoc-ca
`

// newIntegrationCAAndLeaf returns the PEM of a fresh self-signed root CA and a
// leaf server certificate signed by it.
func newIntegrationCAAndLeaf(t *testing.T, commonName string) (caPEM, leafPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("generate CA serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}
	caCert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafSerial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		t.Fatalf("generate leaf serial: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: leafSerial,
		Subject:      pkix.Name{CommonName: "registry.internal"},
		DNSNames:     []string{"registry.internal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caCert, &leafKey.PublicKey, key)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	leafPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})
	return caPEM, leafPEM
}

// newIntegrationCA returns the PEM of a fresh self-signed root CA.
func newIntegrationCA(t *testing.T, commonName string) []byte {
	t.Helper()
	caPEM, _ := newIntegrationCAAndLeaf(t, commonName)
	return caPEM
}

// pemProbeLine returns one full base64 line from the middle of the PEM, a
// string that identifies the certificate inside a bundle.
func pemProbeLine(t *testing.T, certPEM []byte) string {
	t.Helper()
	var full []string
	for _, line := range strings.Split(string(certPEM), "\n") {
		if len(line) == 64 {
			full = append(full, line)
		}
	}
	if len(full) == 0 {
		t.Fatal("CA PEM has no full base64 line")
	}
	return full[len(full)/2]
}

// writeCAOverlayContext writes the build context that makes the test
// Dockerfile expect present as the forwarded CA and reject absent.
func writeCAOverlayContext(t *testing.T, dir string, present, absent, leafPresent, leafAbsent []byte) {
	t.Helper()
	files := map[string][]byte{
		"expected-ca.pem":    present,
		"present.line":       []byte(pemProbeLine(t, present) + "\n"),
		"absent.line":        []byte(pemProbeLine(t, absent) + "\n"),
		"server-present.pem": leafPresent,
		"server-absent.pem":  leafAbsent,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil { //nolint:gosec // G306: copied into the image and read by USER nobody
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func newIntegrationDockerClient(t *testing.T) *dockerclient.Client {
	t.Helper()
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		t.Fatalf("create Docker client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func skipWithoutBuildKit(t *testing.T, cli *dockerclient.Client) {
	t.Helper()
	version, err := builderVersion(context.Background(), cli)
	if err != nil {
		t.Fatalf("builder version: %v", err)
	}
	if err := requireBuildKit(version, &BuildCA{}); err != nil {
		t.Skip("Docker daemon does not report BuildKit: ", err)
	}
}

func removeIntegrationImage(t *testing.T, cli *dockerclient.Client, ref string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = cli.ImageRemove(context.Background(), ref, dockerclient.ImageRemoveOptions{Force: true})
	})
}

// buildCATestBase builds a Debian base with ca-certificates installed (the
// slim image ships without it) and a non-root user, and returns its tag. The
// image only exists in the host daemon, which is where the overlay build
// looks for it.
func buildCATestBase(ctx context.Context, t *testing.T, cli *dockerclient.Client) string {
	t.Helper()
	if err := EnsureImage(ctx, caTestDebianBase); err != nil {
		t.Fatalf("EnsureImage(%s): %v", caTestDebianBase, err)
	}
	dir := t.TempDir()
	dockerfile := "FROM " + caTestDebianBase + "\n" +
		"RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*\n" +
		// A root appended straight to the bundle, not through
		// /usr/local/share/ca-certificates: update-ca-certificates drops it
		// unless jailoc preserves it.
		"RUN openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -keyout /dev/null " +
		"-subj /CN=jailoc-base-root -days 2 -addext basicConstraints=critical,CA:TRUE -out /opt/jailoc-base-root.crt && " +
		"cat /opt/jailoc-base-root.crt >> /etc/ssl/certs/ca-certificates.crt\n" +
		"ENV CACHE_UID=65534\n" +
		"USER nobody\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o600); err != nil {
		t.Fatalf("write base Dockerfile: %v", err)
	}
	buildCtx, err := archive.TarWithOptions(dir, &archive.TarOptions{})
	if err != nil {
		t.Fatalf("tar base build context: %v", err)
	}
	defer func() { _ = buildCtx.Close() }()
	version, err := builderVersion(ctx, cli)
	if err != nil {
		t.Fatalf("builder version: %v", err)
	}
	removeIntegrationImage(t, cli, caTestBaseTag)
	err = imageBuild(ctx, cli, buildCtx, version, dockerclient.ImageBuildOptions{
		Tags:       []string{caTestBaseTag},
		BuildArgs:  proxyBuildArgs(),
		Dockerfile: "Dockerfile",
		Remove:     true,
	}, nil)
	if err != nil {
		t.Fatalf("build CA test base image: %v", err)
	}
	return caTestBaseTag
}

// envMap turns image Config.Env entries into a map.
func envMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// TestBuildOverlayImageCATrust builds a workspace overlay with a forwarded CA
// bundle. The RUN steps of caTestOverlayDockerfile fail the build if the trust
// store, the environment or the secret handling is wrong.
func TestBuildOverlayImageCATrust(t *testing.T) {
	skipWithoutDocker(t)
	cli := newIntegrationDockerClient(t)
	skipWithoutBuildKit(t, cli)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	base := buildCATestBase(ctx, t, cli)

	caA, leafA := newIntegrationCAAndLeaf(t, "jailoc integration CA A")
	caB, leafB := newIntegrationCAAndLeaf(t, "jailoc integration CA B")
	buildCA, err := NewBuildCA(caA)
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	buildDir := t.TempDir()
	writeCAOverlayContext(t, buildDir, caA, caB, leafA, leafB)
	dockerfile := filepath.Join(t.TempDir(), "overlay.Dockerfile")
	if err := os.WriteFile(dockerfile, []byte(caTestOverlayDockerfile), 0o600); err != nil {
		t.Fatalf("write overlay Dockerfile: %v", err)
	}

	tag, err := BuildOverlayImage(ctx, base, workspace.Resolved{
		Name:         "ca-trust",
		Dockerfile:   dockerfile,
		BuildContext: buildDir,
	}, buildCA)
	if err != nil {
		t.Fatalf("BuildOverlayImage: %v", err)
	}
	removeIntegrationImage(t, cli, tag)

	if want := "-ca-" + buildCA.Digest()[:12]; !strings.HasSuffix(tag, want) {
		t.Fatalf("overlay tag %q does not end with %q", tag, want)
	}

	inspect, err := cli.ImageInspect(ctx, tag)
	if err != nil {
		t.Fatalf("inspect %s: %v", tag, err)
	}
	if inspect.Config == nil {
		t.Fatalf("image %s has no config", tag)
	}
	if inspect.Config.User != "nobody" {
		t.Errorf("Config.User = %q, want the base user %q restored", inspect.Config.User, "nobody")
	}
	env := envMap(inspect.Config.Env)
	if env["UV_SYSTEM_CERTS"] != "true" {
		t.Errorf("UV_SYSTEM_CERTS = %q, want true", env["UV_SYSTEM_CERTS"])
	}
	if env["NODE_USE_SYSTEM_CA"] != "1" {
		t.Errorf("NODE_USE_SYSTEM_CA = %q, want 1", env["NODE_USE_SYSTEM_CA"])
	}
	for _, key := range guest.CABundleVars {
		want := guest.SystemCABundle
		if key == "SSL_CERT_FILE" {
			// The Dockerfile's own ENV wins over the injected default.
			want = "/custom/ca.pem"
		}
		if env[key] != want {
			t.Errorf("%s = %q, want %q", key, env[key], want)
		}
	}
}

// TestBuildOverlayImageCAArgOverridesDefaults checks that an ARG with a value
// beats the CA variable defaults jailoc injects: in a descendant stage of the
// same Dockerfile and in an overlay built on an image jailoc built before,
// where the defaults are inherited ENVs. A variable the base sets itself keeps
// precedence over the ARG.
func TestBuildOverlayImageCAArgOverridesDefaults(t *testing.T) {
	skipWithoutDocker(t)
	cli := newIntegrationDockerClient(t)
	skipWithoutBuildKit(t, cli)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	base := buildCATestBase(ctx, t, cli)
	buildCA, err := NewBuildCA(newIntegrationCA(t, "jailoc integration CA"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	build := func(name, dockerfile, from string) (string, map[string]string) {
		t.Helper()
		path := filepath.Join(t.TempDir(), name+".Dockerfile")
		if err := os.WriteFile(path, []byte(dockerfile), 0o600); err != nil {
			t.Fatalf("write %s Dockerfile: %v", name, err)
		}
		tag, err := BuildOverlayImage(ctx, from, workspace.Resolved{Name: name, Dockerfile: path, BuildContext: t.TempDir()}, buildCA)
		if err != nil {
			t.Fatalf("BuildOverlayImage(%s): %v", name, err)
		}
		removeIntegrationImage(t, cli, tag)
		inspect, err := cli.ImageInspect(ctx, tag)
		if err != nil || inspect.Config == nil {
			t.Fatalf("inspect %s: %v", tag, err)
		}
		return tag, envMap(inspect.Config.Env)
	}

	// The build fails when the ARG does not win in the RUN of the child stage.
	_, env := build("ca-arg-stage", `FROM ${BASE} AS parent
FROM parent
ARG REQUESTS_CA_BUNDLE=/custom/arg-ca.pem
RUN test "$REQUESTS_CA_BUNDLE" = /custom/arg-ca.pem && test "$SSL_CERT_FILE" = /etc/ssl/certs/ca-certificates.crt
`, base)
	if env["REQUESTS_CA_BUNDLE"] != "/custom/arg-ca.pem" {
		t.Errorf("stage: REQUESTS_CA_BUNDLE = %q, want the ARG value", env["REQUESTS_CA_BUNDLE"])
	}
	if env["SSL_CERT_FILE"] != guest.SystemCABundle {
		t.Errorf("stage: SSL_CERT_FILE = %q, want the default %q", env["SSL_CERT_FILE"], guest.SystemCABundle)
	}

	// A layer on an image jailoc already injected the defaults into.
	layerBase, _ := build("ca-arg-layer-base", "FROM ${BASE}\nRUN true\n", base)
	_, env = build("ca-arg-layer", `FROM ${BASE}
ARG CURL_CA_BUNDLE=/custom/overlay-arg.pem
RUN test "$CURL_CA_BUNDLE" = /custom/overlay-arg.pem && test "$REQUESTS_CA_BUNDLE" = /etc/ssl/certs/ca-certificates.crt
`, layerBase)
	if env["CURL_CA_BUNDLE"] != "/custom/overlay-arg.pem" {
		t.Errorf("layer: CURL_CA_BUNDLE = %q, want the ARG value", env["CURL_CA_BUNDLE"])
	}

	// A variable the base set itself is not replaced by an ARG.
	explicitBase, _ := build("ca-arg-explicit-base", "FROM ${BASE}\nENV PIP_CERT=/explicit/base.pem\n", base)
	_, env = build("ca-arg-explicit", `FROM ${BASE} AS mid
ARG PIP_CERT=/custom/ignored.pem
FROM mid
RUN test "$PIP_CERT" = /explicit/base.pem
`, explicitBase)
	if env["PIP_CERT"] != "/explicit/base.pem" {
		t.Errorf("explicit: PIP_CERT = %q, want the base's value", env["PIP_CERT"])
	}

	// A RUN before a valued ARG sees jailoc's default, and a RUN after sees the ARG.
	_, env = build("ca-arg-after-run", "FROM $BASE\n"+
		`RUN test "$REQUESTS_CA_BUNDLE" = /etc/ssl/certs/ca-certificates.crt && test "$CURL_CA_BUNDLE" = /etc/ssl/certs/ca-certificates.crt
ARG REQUESTS_CA_BUNDLE=/custom/after-req.pem
ARG CURL_CA_BUNDLE=/custom/after-curl.pem
RUN test "$REQUESTS_CA_BUNDLE" = /custom/after-req.pem && test "$CURL_CA_BUNDLE" = /custom/after-curl.pem
`, base)
	if env["REQUESTS_CA_BUNDLE"] != "/custom/after-req.pem" || env["CURL_CA_BUNDLE"] != "/custom/after-curl.pem" {
		t.Errorf("after-run: REQUESTS_CA_BUNDLE=%q CURL_CA_BUNDLE=%q, want ARG values", env["REQUESTS_CA_BUNDLE"], env["CURL_CA_BUNDLE"])
	}

	// An ARG default in a layer on a jailoc image expands the base image's ENV.
	dirBase, _ := build("ca-arg-dir-base", "FROM $BASE\nENV CA_DIR=/opt/company\n", base)
	_, env = build("ca-arg-dir-layer", "FROM $BASE\n"+
		`ARG CA_DIR=/ignored
ARG SSL_CERT_FILE=${CA_DIR
}/ca.pem
RUN test "$SSL_CERT_FILE" = /opt/company/ca.pem
`, dirBase)
	if env["SSL_CERT_FILE"] != "/opt/company/ca.pem" {
		t.Errorf("dir-layer: SSL_CERT_FILE = %q, want /opt/company/ca.pem", env["SSL_CERT_FILE"])
	}
}

// TestBuildOverlayImageCADigestCacheInvalidation rotates the CA bundle at the
// same Dockerfile path: the new bundle must give a new tag and an image that
// trusts only the new CA.
func TestBuildOverlayImageCADigestCacheInvalidation(t *testing.T) {
	skipWithoutDocker(t)
	cli := newIntegrationDockerClient(t)
	skipWithoutBuildKit(t, cli)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	base := buildCATestBase(ctx, t, cli)

	pemA, leafA := newIntegrationCAAndLeaf(t, "jailoc integration CA A")
	pemB, leafB := newIntegrationCAAndLeaf(t, "jailoc integration CA B")
	caA, err := NewBuildCA(pemA)
	if err != nil {
		t.Fatalf("NewBuildCA(A): %v", err)
	}
	caB, err := NewBuildCA(pemB)
	if err != nil {
		t.Fatalf("NewBuildCA(B): %v", err)
	}

	buildDir := t.TempDir()
	dockerfile := filepath.Join(t.TempDir(), "overlay.Dockerfile")
	if err := os.WriteFile(dockerfile, []byte(caTestOverlayDockerfile), 0o600); err != nil {
		t.Fatalf("write overlay Dockerfile: %v", err)
	}
	ws := workspace.Resolved{Name: "ca-rotate", Dockerfile: dockerfile, BuildContext: buildDir}

	// Each build's RUN checks assert that exactly its own CA is forwarded and
	// the other one is not trusted.
	writeCAOverlayContext(t, buildDir, pemA, pemB, leafA, leafB)
	tagA, err := BuildOverlayImage(ctx, base, ws, caA)
	if err != nil {
		t.Fatalf("BuildOverlayImage with CA A: %v", err)
	}
	removeIntegrationImage(t, cli, tagA)

	writeCAOverlayContext(t, buildDir, pemB, pemA, leafB, leafA)
	tagB, err := BuildOverlayImage(ctx, base, ws, caB)
	if err != nil {
		t.Fatalf("BuildOverlayImage with CA B: %v", err)
	}
	removeIntegrationImage(t, cli, tagB)

	if tagA == tagB {
		t.Fatalf("rotating the CA bundle kept tag %q", tagA)
	}
	if want := "-ca-" + caA.Digest()[:12]; !strings.HasSuffix(tagA, want) {
		t.Errorf("tag A %q does not end with %q", tagA, want)
	}
	if want := "-ca-" + caB.Digest()[:12]; !strings.HasSuffix(tagB, want) {
		t.Errorf("tag B %q does not end with %q", tagB, want)
	}
	prefixA, _, _ := strings.Cut(tagA, "-ca-")
	prefixB, _, _ := strings.Cut(tagB, "-ca-")
	if prefixA != prefixB {
		t.Errorf("same Dockerfile gave different content hashes: %q vs %q", prefixA, prefixB)
	}

	// Rotating back must reproduce the first tag.
	writeCAOverlayContext(t, buildDir, pemA, pemB, leafA, leafB)
	tagAgain, err := BuildOverlayImage(ctx, base, ws, caA)
	if err != nil {
		t.Fatalf("BuildOverlayImage with CA A again: %v", err)
	}
	if tagAgain != tagA {
		t.Errorf("rebuild with CA A gave tag %q, want %q", tagAgain, tagA)
	}
}

// TestBuildOverlayImageCANonDebianRejected checks that a stage without
// /etc/debian_version fails the build with the CA trust error, even when the
// stage is unused and BuildKit would never run it.
func TestBuildOverlayImageCANonDebianRejected(t *testing.T) {
	skipWithoutDocker(t)
	cli := newIntegrationDockerClient(t)
	skipWithoutBuildKit(t, cli)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	for _, ref := range []string{caTestBusyboxBase, caTestDebianBase} {
		if err := EnsureImage(ctx, ref); err != nil {
			t.Fatalf("EnsureImage(%s): %v", ref, err)
		}
	}
	buildCA, err := NewBuildCA(newIntegrationCA(t, "jailoc integration CA"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	tests := []struct {
		name       string
		base       string
		dockerfile string
	}{
		{"non-Debian final stage", caTestBusyboxBase, "FROM ${BASE}\nRUN true\n"},
		{
			"unused non-Debian stage before Debian final stage",
			caTestDebianBase,
			"FROM " + caTestBusyboxBase + " AS unused\nFROM ${BASE}\nRUN true\n",
		},
	}
	// BuildKit reports the script's stderr on the progress display, which
	// writes to os.Stderr, not in the returned error. Capture both.
	// os.Stderr is process-wide, so these subtests must not run in parallel.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dockerfile := filepath.Join(t.TempDir(), "overlay.Dockerfile")
			if err := os.WriteFile(dockerfile, []byte(tt.dockerfile), 0o600); err != nil {
				t.Fatalf("write overlay Dockerfile: %v", err)
			}

			origStderr := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stderr pipe: %v", err)
			}
			captured := make(chan string, 1)
			go func() {
				b, _ := io.ReadAll(r)
				captured <- string(b)
			}()
			os.Stderr = w
			tag, buildErr := BuildOverlayImage(ctx, tt.base, workspace.Resolved{
				Name:       "ca-nondebian",
				Dockerfile: dockerfile,
			}, buildCA)
			os.Stderr = origStderr
			_ = w.Close()
			progress := <-captured
			_ = r.Close()

			if buildErr == nil {
				removeIntegrationImage(t, cli, tag)
				t.Fatalf("expected build of %q with a CA bundle to fail, got tag %q", tt.dockerfile, tag)
			}
			const want = "ca_bundle forwarding supports Debian/Ubuntu build stages only"
			if !strings.Contains(buildErr.Error()+"\n"+progress, want) {
				t.Fatalf("error does not mention %q:\nerror: %v\nprogress: %s", want, buildErr, progress)
			}
		})
	}
}
