//go:build vmintegration

package vmintegration

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/buildkit/session"
	"github.com/moby/moby/api/types/build"
	"github.com/moby/moby/api/types/container"
	dockerclient "github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/jsonmessage"

	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/vm"
)

const (
	// caServerImage is the fixture server image the test builds in the builder VM.
	caServerImage = "jailoc-vmintegration-caserver:latest"
	// caServerReady is the line the fixture prints once it accepts connections.
	caServerReady = "caserver listening"

	caBuildTimeout = 5 * time.Minute
)

// testCA is a throwaway CA that signs the certificates of fixture servers.
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newTestCA(t *testing.T, commonName string) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(t),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate %q: %v", commonName, err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate %q: %v", commonName, err)
	}
	return testCA{cert: cert, key: key, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

func randomSerial(t *testing.T) *big.Int {
	t.Helper()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatalf("random certificate serial: %v", err)
	}
	return serial
}

// issueServer returns a TLS certificate and key (PEM) signed by the CA for
// every address docker0 of the builder VM can hand a container (172.17.0.2
// to 172.17.0.31): the test learns the address of a fixture server only after
// starting it, and the daemon connects to it by that address.
func (ca testCA) issueServer(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	var ips []net.IP
	for i := byte(2); i < 32; i++ {
		ips = append(ips, net.IPv4(172, 17, 0, i))
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(t),
		Subject:      pkix.Name{CommonName: "registry.jailoc.test"},
		DNSNames:     []string{"registry.jailoc.test"},
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("create server certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

// buildCAServer compiles the fixture server for the builder VM, which has the
// architecture of the host.
func buildCAServer(t *testing.T) string {
	t.Helper()
	root, err := projectRoot()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "server")
	cmd := exec.Command("go", "build", "-tags", "vmintegration", "-trimpath", "-buildvcs=false", "-ldflags", "-s -w", "-o", out, "./internal/vmintegration/caserver") //nolint:gosec // G204: fixed go subcommand
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build CA fixture server: %v\n%s", err, combined)
	}
	return out
}

// buildContext packs files (name to content) into a Docker build context.
func buildContext(files map[string][]byte) (io.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("write build context header %q: %w", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, fmt.Errorf("write build context file %q: %w", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("close build context: %w", err)
	}
	return &buf, nil
}

// builderBuild runs a BuildKit build of files (which hold the Dockerfile) in
// the builder VM's dockerd. It always re-resolves base images and ignores the
// build cache, so a fetch that succeeded on an earlier boot cannot answer for
// this one: the data disk keeps the cache across boots.
func builderBuild(ctx context.Context, cli *dockerclient.Client, files map[string][]byte, tags ...string) error {
	ctx, cancel := context.WithTimeout(ctx, caBuildTimeout)
	defer cancel()
	buildCtx, err := buildContext(files)
	if err != nil {
		return err
	}
	// BuildKit resolves images through a client session, as docker build does.
	sess, err := session.NewSession(ctx, "")
	if err != nil {
		return fmt.Errorf("create BuildKit session: %w", err)
	}
	go func() {
		_ = sess.Run(ctx, func(ctx context.Context, proto string, meta map[string][]string) (net.Conn, error) {
			return cli.DialHijack(ctx, "/session", proto, meta)
		})
	}()
	defer func() { _ = sess.Close() }()

	resp, err := cli.ImageBuild(ctx, buildCtx, dockerclient.ImageBuildOptions{
		Version:     build.BuilderBuildKit,
		SessionID:   sess.ID(),
		Dockerfile:  "Dockerfile",
		Tags:        tags,
		Remove:      true,
		ForceRemove: true,
		NoCache:     true,
		PullParent:  true,
	})
	if err != nil {
		return fmt.Errorf("start build: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	// The stream ends with an error message when the build fails.
	if err := jsonmessage.DisplayJSONMessagesStream(resp.Body, io.Discard, 0, false, nil); err != nil {
		return fmt.Errorf("build: %w", err)
	}
	return nil
}

func dockerfileBuild(dockerfile string) map[string][]byte {
	return map[string][]byte{"Dockerfile": []byte(dockerfile)}
}

// isCertError reports whether err is a TLS verification failure, not just any
// failed fetch.
func isCertError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "x509") || strings.Contains(msg, "certificate")
}

// caFixture is a fixture server in the builder VM.
type caFixture struct {
	ip string
}

// startCAServer starts the fixture server image with a certificate signed by
// ca and returns it once it accepts connections. The returned stop removes
// the container.
func startCAServer(ctx context.Context, t *testing.T, cli *dockerclient.Client, ca testCA) (caFixture, func()) {
	t.Helper()
	certPEM, keyPEM := ca.issueServer(t)
	created, err := cli.ContainerCreate(ctx, dockerclient.ContainerCreateOptions{
		Image: caServerImage,
		Config: &container.Config{
			Env: []string{"TLS_CERT=" + string(certPEM), "TLS_KEY=" + string(keyPEM)},
		},
	})
	if err != nil {
		t.Fatalf("create CA fixture server: %v", err)
	}
	stop := func() {
		if _, err := cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, dockerclient.ContainerRemoveOptions{Force: true}); err != nil {
			t.Errorf("remove CA fixture server %s: %v", created.ID, err)
		}
	}
	if _, err := cli.ContainerStart(ctx, created.ID, dockerclient.ContainerStartOptions{}); err != nil {
		stop()
		t.Fatalf("start CA fixture server: %v", err)
	}
	inspected, err := cli.ContainerInspect(ctx, created.ID, dockerclient.ContainerInspectOptions{})
	if err != nil {
		stop()
		t.Fatalf("inspect CA fixture server: %v", err)
	}
	var ip string
	if inspected.Container.NetworkSettings != nil {
		if ep := inspected.Container.NetworkSettings.Networks["bridge"]; ep != nil && ep.IPAddress.IsValid() {
			ip = ep.IPAddress.String()
		}
	}
	if ip == "" {
		stop()
		t.Fatalf("CA fixture server has no bridge address: %+v", inspected.Container.NetworkSettings)
	}
	if err := waitCAServer(ctx, cli, created.ID); err != nil {
		stop()
		t.Fatal(err)
	}
	return caFixture{ip: ip}, stop
}

// waitCAServer waits for the fixture to log that it listens.
func waitCAServer(ctx context.Context, cli *dockerclient.Client, id string) error {
	deadline := time.Now().Add(30 * time.Second)
	var logs []byte
	for time.Now().Before(deadline) {
		rc, err := cli.ContainerLogs(ctx, id, dockerclient.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
		if err != nil {
			return fmt.Errorf("read CA fixture server logs: %w", err)
		}
		logs, err = io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			return fmt.Errorf("read CA fixture server logs: %w", err)
		}
		if bytes.Contains(logs, []byte(caServerReady)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for CA fixture server: %w", ctx.Err())
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("CA fixture server not listening within 30s; logs:\n%s", logs)
}

// caFetch is one kind of fetch a managed builder does over TLS from a server
// at ip. phase makes the reference unique per boot.
type caFetch struct {
	name       string
	dockerfile func(ip string, phase int) string
}

var caFetches = []caFetch{
	{"FROM private-CA registry", func(ip string, phase int) string {
		return fmt.Sprintf("FROM %s/test/base:p%d\nLABEL jailoc.test=from\n", ip, phase)
	}},
	{"COPY --from private-CA registry", func(ip string, phase int) string {
		return fmt.Sprintf("FROM scratch\nCOPY --from=%s/test/copy:p%d /from-registry.txt /out.txt\n", ip, phase)
	}},
	{"HTTPS remote ADD", func(ip string, phase int) string {
		return fmt.Sprintf("FROM scratch\nADD https://%s/add.txt?phase=%d /add.txt\n", ip, phase)
	}},
}

// TestBuilderVMCABundle boots the shared builder VM three times on one state
// directory with a CA bundle forwarded (CA A), rotated (CA B) and disabled,
// and in each boot lets its dockerd and BuildKit fetch over TLS from fixture
// servers whose certificates are signed by CA A and by CA B: a registry pull
// for FROM, an external COPY --from image, a remote ADD over HTTPS, the
// standard Dockerfile frontend and a public image. A fetch must succeed
// exactly from the servers whose CA the boot forwarded; public roots stay
// trusted in every boot. The second and third boot show that the guest init
// leaves nothing of the previous bundle in the system roots.
func TestBuilderVMCABundle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Minute)
	defer cancel()

	caA := newTestCA(t, "jailoc vmintegration CA A")
	caB := newTestCA(t, "jailoc vmintegration CA B")
	bundle := func(name string, testCert testCA) (string, *docker.BuildCA) {
		t.Helper()
		path := filepath.Join(home, name)
		if err := os.WriteFile(path, testCert.pem, 0o600); err != nil {
			t.Fatalf("write CA bundle %s: %v", name, err)
		}
		ca, err := docker.NewBuildCA(testCert.pem)
		if err != nil {
			t.Fatalf("CA bundle %s: %v", name, err)
		}
		return path, ca
	}
	pathA, buildCAA := bundle("vmca-a.pem", caA)
	pathB, buildCAB := bundle("vmca-b.pem", caB)
	serverBin := buildCAServer(t)

	// The state directory holds unix sockets (103 bytes at most): keep it short.
	dir := filepath.Join(home, "bldca")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "supervisor.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: test state file
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logFile.Close() })
	t.Cleanup(func() {
		if t.Failed() {
			dumpBuilderLogs(t, dir)
		}
	})

	spec := vm.Spec{
		Kind:       vm.KindBuilder,
		Workspace:  "builder",
		StateDir:   dir,
		Image:      vm.DefaultImage,
		CPUs:       2,
		MemoryMiB:  2048,
		DiskSize:   builderDiskInitial,
		Policy:     []byte(builderPolicy),
		RunnerPath: os.Getenv("JAILOC_VM_RUNNER"),
		LibDir:     os.Getenv("JAILOC_VM_LIBDIR"),
		ImageCache: filepath.Join(home, ".cache", "jailoc-vm", "images"),
		Broker:     []string{binaryPath, "netbroker"},
		Log:        logFile,
	}

	phases := []struct {
		name    string
		path    string
		ca      *docker.BuildCA
		trustA  bool
		trustB  bool
		timeout time.Duration
	}{
		{name: "forwarded CA A", path: pathA, ca: buildCAA, trustA: true, timeout: builderFirstBootTimeout},
		{name: "rotated to CA B", path: pathB, ca: buildCAB, trustB: true, timeout: builderRebootTimeout},
		{name: "forwarding disabled", timeout: builderRebootTimeout},
	}
	for i, p := range phases {
		phase := i + 1
		t.Run(p.name, func(t *testing.T) {
			spec.CABundle, spec.CABundleDigest = p.path, p.ca.Digest()
			caBundlePhase(ctx, t, dir, spec, p.timeout, phase, serverBin, caA, caB, p.trustA, p.trustB)
		})
	}
}

// caBundlePhase is one boot of the shared builder with spec. trustA and trustB
// say whether the boot must trust the CA of the fixture server A and B.
func caBundlePhase(ctx context.Context, t *testing.T, dir string, spec vm.Spec, timeout time.Duration, phase int, serverBin string, caA, caB testCA, trustA, trustB bool) {
	t.Helper()
	// A state file left by the previous boot must not look like this one.
	if err := os.Remove(vm.StateFile(dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("remove stale builder state: %v", err)
	}
	if err := vm.WriteSpec(spec); err != nil {
		t.Fatalf("write builder spec: %v", err)
	}
	start := time.Now()
	run := startBuilder(ctx, spec)
	defer func() {
		if err := run.stop(); err != nil {
			t.Errorf("stop builder: %v", err)
		}
	}()
	st, err := waitBuilderReady(ctx, run, dir, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("builder ready after %s (CA bundle %q)", time.Since(start).Round(time.Second), spec.CABundle)

	cli, err := dockerclient.New(dockerclient.WithHost("unix://" + st.DockerSocket))
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer func() { _ = cli.Close() }()

	// The fixture server is built from scratch, so the build needs no pull.
	serverBuild := map[string][]byte{
		"Dockerfile": []byte("FROM scratch\nCOPY server /server\nENTRYPOINT [\"/server\"]\n"),
	}
	bin, err := os.ReadFile(serverBin) //nolint:gosec // G304: binary the test built
	if err != nil {
		t.Fatalf("read CA fixture server: %v", err)
	}
	serverBuild["server"] = bin
	if err := builderBuild(ctx, cli, serverBuild, caServerImage); err != nil {
		t.Fatalf("build CA fixture server image: %v", err)
	}

	servers := []struct {
		name  string
		ca    testCA
		trust bool
	}{
		{"server signed by CA A", caA, trustA},
		{"server signed by CA B", caB, trustB},
	}
	ips := make([]string, len(servers))
	for i, s := range servers {
		fixture, stop := startCAServer(ctx, t, cli, s.ca)
		defer stop() //nolint:gocritic // deferInLoop: removes both fixtures when the boot ends
		ips[i] = fixture.ip
	}

	for _, f := range caFetches {
		t.Run(f.name, func(t *testing.T) {
			for i, s := range servers {
				t.Run(s.name, func(t *testing.T) {
					err := builderBuild(ctx, cli, dockerfileBuild(f.dockerfile(ips[i], phase)))
					expectCATrust(t, err, s.trust)
				})
			}
		})
	}

	// Public roots stay trusted next to a forwarded bundle, and are all
	// there is when forwarding is off.
	public := []struct {
		name       string
		dockerfile string
	}{
		{"standard frontend resolution", fmt.Sprintf("# syntax=docker/dockerfile:1\nFROM scratch\nLABEL jailoc.test=frontend%d\n", phase)},
		{"public registry FROM", fmt.Sprintf("FROM debian:bookworm-slim\nLABEL jailoc.test=public%d\n", phase)},
	}
	for _, p := range public {
		t.Run(p.name, func(t *testing.T) {
			if err := builderBuild(ctx, cli, dockerfileBuild(p.dockerfile)); err != nil {
				t.Errorf("public fetch failed: %v", err)
			}
		})
	}
}

// expectCATrust checks that a fetch from a server succeeded if its CA is
// trusted and failed on certificate verification if not.
func expectCATrust(t *testing.T, err error, trusted bool) {
	t.Helper()
	switch {
	case trusted && err != nil:
		t.Errorf("fetch from a server with a trusted CA failed: %v", err)
	case !trusted && err == nil:
		t.Error("fetch from a server with an untrusted CA succeeded")
	case !trusted && !isCertError(err):
		t.Errorf("fetch from a server with an untrusted CA failed, but not on certificate verification: %v", err)
	}
}
