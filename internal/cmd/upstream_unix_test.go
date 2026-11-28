//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func upstreamTestWorkspace() *workspace.Resolved {
	return &workspace.Resolved{
		Name: "api", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g",
		UpstreamProxy: &config.UpstreamProxy{
			URL:  "http://proxy.example:3128",
			Auth: &config.UpstreamAuth{Username: "u", FromCommand: "pass show proxy", TTL: "1h"},
		},
	}
}

func decodeUpstreamPolicy(t *testing.T, data []byte) *netbroker.UpstreamProxy {
	t.Helper()
	var p struct {
		UpstreamProxy *netbroker.UpstreamProxy `json:"upstream_proxy"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("decode policy %s: %v", data, err)
	}
	return p.UpstreamProxy
}

func TestVMPolicyUpstreamProxy(t *testing.T) {
	t.Parallel()
	ws := upstreamTestWorkspace()
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	got := decodeUpstreamPolicy(t, data)
	if got == nil || got.Workspace != "api" || got.URL != "http://proxy.example:3128" || got.Auth == nil || got.Auth.FromCommand != "pass show proxy" || got.Auth.TTL != "1h" {
		t.Fatalf("upstream_proxy = %+v in %s", got, data)
	}
	builder, err := vmBuilderPolicy(context.Background(), ws)
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeUpstreamPolicy(t, builder); got != nil {
		t.Fatalf("isolated builder policy carries the workspace proxy: %s", builder)
	}
	plain, err := vmPolicy(context.Background(), &workspace.Resolved{Name: "w"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("upstream_proxy")) {
		t.Fatalf("policy without proxy mentions it: %s", plain)
	}
}

func TestWithUpstreamProxy(t *testing.T) {
	t.Parallel()
	proxy := &netbroker.UpstreamProxy{Workspace: "builder", URL: "http://proxy.example:3128"}
	for _, base := range []string{`{}`, `{"deny":[],"allow":[{"name":"r","proto":"tcp","names":["example.com"]}]}`} {
		got, err := withUpstreamProxy([]byte(base), proxy)
		if err != nil {
			t.Fatal(err)
		}
		if p := decodeUpstreamPolicy(t, got); p == nil || p.Workspace != "builder" || p.URL != proxy.URL {
			t.Fatalf("withUpstreamProxy(%s) = %s", base, got)
		}
		if strings.Contains(base, "example.com") && !bytes.Contains(got, []byte("example.com")) {
			t.Fatalf("withUpstreamProxy dropped rules: %s", got)
		}
	}
	same, err := withUpstreamProxy([]byte(`{}`), nil)
	if err != nil || string(same) != `{}` {
		t.Fatalf("withUpstreamProxy(nil) = %s, %v", same, err)
	}
}

func TestRequiredBrokerPolicyUpstream(t *testing.T) {
	t.Parallel()
	if got := requiredBrokerPolicy(upstreamTestWorkspace()); got != policyVersionUpstream {
		t.Fatalf("requiredBrokerPolicy = %d, want %d", got, policyVersionUpstream)
	}
	if policyVersionUpstream != 4 || netbroker.PolicyVersion < policyVersionUpstream {
		t.Fatalf("policyVersionUpstream = %d, broker %d", policyVersionUpstream, netbroker.PolicyVersion)
	}
}

func TestReloadPolicyUpstreamNeedsCurrentBrokerWithoutRules(t *testing.T) {
	t.Parallel()
	ws := upstreamTestWorkspace()
	old := vm.Spec{GuestProtocol: guest.Protocol, Guest: &guest.Config{}, BrokerPolicy: policyVersionQuiet}
	if _, err := reloadPolicy(context.Background(), ws, old); err == nil {
		t.Fatal("reloadPolicy accepted the upstream proxy on a version 3 broker")
	}
	old.BrokerPolicy = policyVersionUpstream
	if _, err := reloadPolicy(context.Background(), ws, old); err != nil {
		t.Fatalf("reloadPolicy on a version 4 broker: %v", err)
	}
}

func TestWorkspaceSpecUpstreamCABundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	ws := upstreamTestWorkspace()
	spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !spec.UpstreamProxy || spec.CABundle != "" || spec.Guest.CABundle != vm.GuestUpstreamBundle {
		t.Fatalf("no base: UpstreamProxy=%v CABundle=%q guest=%q", spec.UpstreamProxy, spec.CABundle, spec.Guest.CABundle)
	}
	payload, err := secretsPayload(context.Background(), &workspace.Resolved{Name: "api"}, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(payload.Env, "\n"), "SSL_CERT_FILE="+guest.SystemCABundle) {
		t.Fatalf("CA env missing for the predeclared bundle: %v", payload.Env)
	}

	base := filepath.Join(config.ConfigDir(), "workspaces", ws.Name, caBundleFilename)
	if err := os.MkdirAll(filepath.Dir(base), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestCABundle(t, base, "base")
	spec, err = workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.CABundle != base || spec.Guest.CABundle != vm.GuestUpstreamBundle {
		t.Fatalf("with base: CABundle=%q guest=%q", spec.CABundle, spec.Guest.CABundle)
	}

	plain := upstreamTestWorkspace()
	plain.UpstreamProxy = nil
	spec, err = workspaceSpec(t.Context(), plain, vmRuntimeFiles{}, "image", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if spec.UpstreamProxy || spec.CABundle != "" || spec.Guest.CABundle != filepath.Join(guestConfigDir, caBundleFilename) {
		t.Fatalf("without proxy: UpstreamProxy=%v CABundle=%q guest=%q", spec.UpstreamProxy, spec.CABundle, spec.Guest.CABundle)
	}
}

func TestUpstreamReloadWarning(t *testing.T) {
	t.Parallel()
	var ev brokerReload
	if err := json.Unmarshal([]byte(`{"event":"reload","upstream_error":"helper exited\u001b[31m","upstream_stderr":"token=hunter2","upstream_generation":3}`), &ev); err != nil {
		t.Fatal(err)
	}
	got := upstreamReloadWarning(ev)
	if !strings.Contains(got, "helper exited") || !strings.Contains(got, "generation 3") {
		t.Fatalf("upstreamReloadWarning = %q", got)
	}
	if strings.Contains(got, "hunter2") || strings.Contains(got, "\x1b") {
		t.Fatalf("upstreamReloadWarning leaks stderr or control characters: %q", got)
	}
	if got := upstreamReloadWarning(brokerReload{Event: "reload"}); got != "" {
		t.Fatalf("warning without upstream error = %q", got)
	}
}

func TestBrokerReloadTimeout(t *testing.T) {
	t.Parallel()
	if got := brokerReloadTimeout(vm.Spec{}, vm.Spec{}); got != reloadTimeout {
		t.Fatalf("plain = %s", got)
	}
	if got := brokerReloadTimeout(vm.Spec{}, vm.Spec{UpstreamProxy: true}); got != upstreamReloadTimeout {
		t.Fatalf("enabling = %s", got)
	}
	if got := brokerReloadTimeout(vm.Spec{UpstreamProxy: true}, vm.Spec{}); got != upstreamReloadTimeout {
		t.Fatalf("disabling = %s", got)
	}
	if upstreamReloadTimeout != 35*time.Second {
		t.Fatalf("upstreamReloadTimeout = %s", upstreamReloadTimeout)
	}
}

func TestPrintUpstreamStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var buf bytes.Buffer
	printUpstreamStatus(&buf, dir)
	if !strings.Contains(buf.String(), "unknown") {
		t.Fatalf("missing status = %q", buf.String())
	}
	status := `{"version":1,"state":"degraded","mode":"command","url":"http://127.0.0.1:3128","generation":2,"header_names":["Proxy-Authorization"],"last_error":"helper exited 1","last_stderr":"secret=hunter2","updated":"2026-11-05T12:00:00Z"}`
	if err := os.WriteFile(vm.UpstreamStatusFile(dir), []byte(status), 0o600); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	printUpstreamStatus(&buf, dir)
	out := buf.String()
	for _, want := range []string{"Upstream:", "degraded", "helper exited 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("status %q lacks %q", out, want)
		}
	}
	for _, leak := range []string{"hunter2", "Proxy-Authorization"} {
		if strings.Contains(out, leak) {
			t.Errorf("status %q leaks %q", out, leak)
		}
	}
}

func TestReloadUpstreamCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir := vmStateDir("api")
	base := filepath.Join(t.TempDir(), "base.pem")
	baseCA := writeTestCABundle(t, base, "base")
	tz := &guest.TimeZone{Name: "Europe/Prague"}
	spec := vm.Spec{
		Kind: vm.KindWorkspace, Workspace: "api", StateDir: stateDir, UpstreamProxy: true, CABundle: base,
		Guest: &guest.Config{CABundle: vm.GuestUpstreamBundle, SSHAgent: true, HostPorts: []uint16{5432}, TimeZone: tz, Tmp: guest.TmpConfig{}},
	}
	if err := vm.WriteSpec(spec); err != nil {
		t.Fatal(err)
	}
	upstreamCA := writeTestCABundle(t, vm.UpstreamCAFile(stateDir), "upstream")
	// The VM creates the control directory when it boots.
	if err := os.MkdirAll(filepath.Join(vm.GuestDataDir(stateDir), vm.ControlName), 0o700); err != nil {
		t.Fatal(err)
	}

	reqs := make(chan guest.Reload, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		for ctx.Err() == nil {
			if b, err := vm.ReadControl(stateDir, guest.ReloadFile); err == nil {
				var r guest.Reload
				if json.Unmarshal(b, &r) == nil {
					done, _ := json.Marshal(guest.ReloadDone{Seq: r.Seq})
					_ = vm.WriteControl(stateDir, guest.ReloadDoneFile, done)
					reqs <- r
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	if err := upstreamCAReloader(stateDir, "api")(ctx); err != nil {
		t.Fatalf("reload upstream CA: %v", err)
	}
	r := <-reqs
	if r.CABundle != vm.GuestUpstreamBundle || !r.SSHAgent || len(r.HostPorts) != 1 || r.TimeZone == nil || r.TmpTTL != nil || r.ProxyPorts != nil {
		t.Fatalf("reload request %+v does not keep the running settings", r)
	}
	root, err := vm.OpenGuestReadOnly(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	bundle, err := root.ReadFile(filepath.Base(vm.GuestUpstreamBundle))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(bundle, bytes.TrimSpace(baseCA.data)) || !bytes.Contains(bundle, bytes.TrimSpace(upstreamCA.data)) {
		t.Fatalf("composed bundle lacks the base or the upstream CA")
	}
}

func TestReloadUpstreamCAWithoutProxyIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	stateDir := vmStateDir("api")
	if err := vm.WriteSpec(vm.Spec{Kind: vm.KindWorkspace, Workspace: "api", StateDir: stateDir, Guest: &guest.Config{}}); err != nil {
		t.Fatal(err)
	}
	if err := upstreamCAReloader(stateDir, "api")(t.Context()); err != nil {
		t.Fatalf("reload without proxy: %v", err)
	}
	if _, err := vm.ReadControl(stateDir, guest.ReloadFile); err == nil {
		t.Fatal("reload without proxy asked the guest to reload")
	}
}

func TestSupervisorEnvCarriesConfigFlag(t *testing.T) {
	t.Parallel()
	env := []string{"HOME=/h", config.PathEnv + "=/old.toml"}
	if got := supervisorEnv(env, ""); strings.Join(got, ",") != strings.Join(env, ",") {
		t.Fatalf("supervisorEnv without flag = %v", got)
	}
	got := supervisorEnv(env, "/flag.toml")
	var paths []string
	for _, e := range got {
		if v, ok := strings.CutPrefix(e, config.PathEnv+"="); ok {
			paths = append(paths, v)
		}
	}
	if len(paths) != 1 || paths[0] != config.ConfigPath() || env[1] != config.PathEnv+"=/old.toml" {
		t.Fatalf("supervisorEnv = %v (input %v)", got, env)
	}
}
func TestVMFailureIncludesBrokerStartupLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "supervisor.log")
	if err := os.WriteFile(logPath, []byte("jailoc-netbroker: start upstream: upstream helper start: exit status 1; stderr: run my-helper login first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(dir), []byte(`{"pid":1,"ready":false,"error":"start network broker: broker exited before ready: exit status 1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := vmFailure(dir, logPath)
	if !strings.Contains(got, "run my-helper login first") {
		t.Fatalf("vmFailure = %q, want broker startup log included", got)
	}
}

func TestBuilderCABundleDigestRotatesOnUpstreamCA(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	caPath := filepath.Join(dir, "proxy-ca.pem")
	if err := os.WriteFile(caPath, []byte("cert-v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseCA, err := docker.NewBuildCA([]byte("base-cert\n"))
	if err != nil {
		t.Fatal(err)
	}
	up := &netbroker.UpstreamProxy{Workspace: "builder", URL: "http://proxy.example:3128", CA: caPath}

	d1 := builderCABundleDigest(baseCA, up)
	if d1 == "" || d1 == baseCA.Digest() {
		t.Fatalf("builderCABundleDigest with upstream CA = %q, base = %q", d1, baseCA.Digest())
	}
	dOnlyUp := builderCABundleDigest(nil, up)
	if dOnlyUp == "" || dOnlyUp == d1 {
		t.Fatalf("builderCABundleDigest without base CA = %q, with base = %q", dOnlyUp, d1)
	}
	if err := os.WriteFile(caPath, []byte("cert-v2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d2 := builderCABundleDigest(baseCA, up)
	if d2 == "" || d2 == d1 {
		t.Fatalf("builderCABundleDigest after rotating upstream CA = %q, before = %q", d2, d1)
	}
}
