//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestFormatImportProgress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    vm.ImportProgress
		want string
	}{
		{name: "save starting", p: vm.ImportProgress{Phase: vm.PhaseSave}, want: "  ⠋ Exporting image from Docker… 3s"},
		{name: "save bytes", p: vm.ImportProgress{Phase: vm.PhaseSave, Bytes: 128 << 20}, want: "  ⠋ Exporting image from Docker (128 MiB)… 3s"},
		{name: "extract layers", p: vm.ImportProgress{Phase: vm.PhaseExtract, Layers: 2, Total: 5}, want: "  ⠋ Converting image layers (2/5)… 3s"},
		{name: "compose", p: vm.ImportProgress{Phase: vm.PhaseExtract}, want: "  ⠋ Linking image layers… 3s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatImportProgress('⠋', tt.p, 3); got != tt.want {
				t.Errorf("formatImportProgress = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "less than kibibyte", bytes: 511, want: "< 1 KiB"},
		{name: "kibibytes", bytes: 512 << 10, want: "512 KiB"},
		{name: "less than mebibyte", bytes: (1 << 20) - 1, want: "1023 KiB"},
		{name: "mebibytes", bytes: 128 << 20, want: "128 MiB"},
		{name: "gibibytes", bytes: 3 << 29, want: "1.5 GiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := formatBytes(tt.bytes); got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestImportProgressDisplayWritesOnlyForTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		interactive bool
		want        string
	}{
		{name: "non-terminal"},
		{name: "terminal", interactive: true, want: "\r\x1b[2K  ⠋ Converting image layers (1/2)… 0s\r\x1b[2K"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var out bytes.Buffer
			display := newImportProgressDisplay(&out, tt.interactive, func() int { return 80 })
			display.update(vm.ImportProgress{Phase: vm.PhaseExtract, Layers: 1, Total: 2})
			display.redraw()
			display.erase()
			if got := out.String(); got != tt.want {
				t.Errorf("display output = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestImportProgressDisplayTruncatesToTerminalWidth(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	display := newImportProgressDisplay(&out, true, func() int { return 12 })
	display.update(vm.ImportProgress{Phase: vm.PhaseSave, Bytes: 128 << 20})
	display.redraw()
	if got, want := out.String(), "\r\x1b[2K  ⠋ Exporti…"; got != want {
		t.Errorf("display output = %q, want %q", got, want)
	}
}

func TestUseHostDocker(t *testing.T) {
	t.Parallel()
	unusable := errors.New("docker daemon architecture is x86_64, VMs need aarch64")
	tests := []struct {
		name      string
		builder   string
		isolated  bool
		forwardCA bool
		hostErr   error
		want      bool
		wantErr   bool
		wantMsg   []string
	}{
		{name: "auto default uses host", builder: "", want: true},
		{name: "auto uses host", builder: config.BuilderAuto, want: true},
		{name: "auto falls back to vm", builder: config.BuilderAuto, hostErr: unusable},
		{name: "docker uses host", builder: config.BuilderDocker, want: true},
		{name: "docker without host fails", builder: config.BuilderDocker, hostErr: unusable, wantErr: true},
		{name: "vm never uses host", builder: config.BuilderVM},
		{name: "isolated build uses its vm", builder: config.BuilderDocker, isolated: true},
		{name: "auto with CA routes to builder vm", builder: config.BuilderAuto, forwardCA: true},
		{name: "default with CA routes to builder vm", builder: "", forwardCA: true},
		{name: "vm with CA", builder: config.BuilderVM, forwardCA: true},
		{name: "docker with CA is rejected", builder: config.BuilderDocker, forwardCA: true, wantErr: true, wantMsg: []string{`builder = "auto"`, "ca_bundle = false"}},
		{name: "docker with CA and unusable host is rejected", builder: config.BuilderDocker, forwardCA: true, hostErr: unusable, wantErr: true, wantMsg: []string{"ca_bundle = false"}},
		{name: "isolated docker with CA is rejected", builder: config.BuilderDocker, isolated: true, forwardCA: true, wantErr: true, wantMsg: []string{`builder = "auto"`, "ca_bundle = false"}},
		{name: "isolated auto with CA uses its vm", builder: config.BuilderAuto, isolated: true, forwardCA: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := useHostDocker(tt.builder, tt.isolated, tt.forwardCA, tt.hostErr)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("useHostDocker = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
			for _, msg := range tt.wantMsg {
				if !strings.Contains(err.Error(), msg) {
					t.Errorf("error %q does not mention %q", err, msg)
				}
			}
		})
	}
}

func TestWriteBuilderCABundle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	pemData := []byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n")
	ca, err := docker.NewBuildCA(pemData)
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	want := filepath.Join(dir, caBundleFilename)

	got, err := writeBuilderCABundle(dir, ca)
	if err != nil {
		t.Fatalf("writeBuilderCABundle: %v", err)
	}
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	content, err := os.ReadFile(want) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	if !bytes.Equal(content, pemData) {
		t.Errorf("bundle content = %q, want %q", content, pemData)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("bundle mode = %o, want 600", perm)
	}

	got, err = writeBuilderCABundle(dir, nil)
	if err != nil {
		t.Fatalf("writeBuilderCABundle(nil): %v", err)
	}
	if got != "" {
		t.Errorf("path for nil CA = %q, want empty", got)
	}
	if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale bundle still present: %v", err)
	}

	// Removing an already absent bundle is not an error.
	if _, err := writeBuilderCABundle(dir, nil); err != nil {
		t.Errorf("second writeBuilderCABundle(nil): %v", err)
	}
}

func TestWorkspaceSpecWorkDir(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		paths []string
		want  string
	}{
		{name: "no paths uses guest home", want: "/home/agent"},
		{name: "paths use first", paths: []string{"/work/first", "/work/second"}, want: "/work/first"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "test", Paths: tt.paths, CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g"}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if spec.Guest.WorkDir != tt.want {
				t.Errorf("WorkDir = %q, want %q", spec.Guest.WorkDir, tt.want)
			}
		})
	}
}

func TestSecretsPayload(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{
		Name: "ws",
		Env:  []string{"PLAIN=1"},
		Secrets: []workspace.ResolvedSecret{
			{Name: "TOKEN", Kind: workspace.SecretKindEnv, FromCommand: "echo env-value"},
			{Name: "netrc", Kind: workspace.SecretKindFile, FromCommand: "echo file-value"},
		},
	}
	spec := vm.Spec{SSHAgent: "/tmp/agent.sock", Guest: &guest.Config{CABundle: "/ca.pem"}}
	got, err := secretsPayload(context.Background(), ws, spec, false)
	if err != nil {
		t.Fatalf("secretsPayload: %v", err)
	}
	wantEnv := []string{"PLAIN=1", "SSH_AUTH_SOCK=" + guest.SSHAgentSocket, "NODE_USE_SYSTEM_CA=1", "TOKEN=env-value"}
	wantEnv = append(wantEnv, caBundleEnv()...)
	if !slices.Equal(got.Env, wantEnv) {
		t.Errorf("Env = %q, want %q", got.Env, wantEnv)
	}
	if len(got.Files) != 1 || string(got.Files["netrc"]) != "file-value\n" {
		t.Errorf("Files = %q, want only netrc with the command output", got.Files)
	}
}

func TestSecretsPayloadCABundleEnv(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		env      []string
		caBundle string
		want     map[string]string
		absent   []string
	}{
		{
			name:     "no bundle",
			absent:   append([]string{"NODE_USE_SYSTEM_CA"}, guest.CABundleVars...),
			caBundle: "",
		},
		{
			name:     "bundle",
			caBundle: "/etc/jailoc/ca-bundle.pem",
			want: map[string]string{
				"NODE_USE_SYSTEM_CA":  "1",
				"NODE_EXTRA_CA_CERTS": guest.SystemCABundle,
				"SSL_CERT_FILE":       guest.SystemCABundle,
				"REQUESTS_CA_BUNDLE":  guest.SystemCABundle,
				"GIT_SSL_CAINFO":      guest.SystemCABundle,
			},
		},
		{
			name:     "workspace env wins",
			env:      []string{"REQUESTS_CA_BUNDLE=/custom.pem"},
			caBundle: "/etc/jailoc/ca-bundle.pem",
			want: map[string]string{
				"REQUESTS_CA_BUNDLE": "/custom.pem",
				"SSL_CERT_FILE":      guest.SystemCABundle,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "ws", Env: tt.env}
			spec := vm.Spec{Guest: &guest.Config{CABundle: tt.caBundle}}
			got, err := secretsPayload(context.Background(), ws, spec, false)
			if err != nil {
				t.Fatalf("secretsPayload: %v", err)
			}
			values := map[string][]string{}
			for _, e := range got.Env {
				k, v, _ := strings.Cut(e, "=")
				values[k] = append(values[k], v)
			}
			for k, v := range tt.want {
				if !slices.Equal(values[k], []string{v}) {
					t.Errorf("%s = %q, want exactly %q", k, values[k], v)
				}
			}
			for _, k := range tt.absent {
				if _, ok := values[k]; ok {
					t.Errorf("%s set without a CA bundle", k)
				}
			}
		})
	}
}

func TestStatusRestartChanges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g", ProtectGit: true}
	matching := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol, Guest: &guest.Config{NoPrivilegeEscalation: true}}
	if got := statusRestartChanges(ws, matching); len(got) != 0 {
		t.Errorf("matching spec: got %v, want no changes", got)
	}
	if got := statusRestartChanges(ws, vm.Spec{GuestProtocol: guest.Protocol, Guest: &guest.Config{NoPrivilegeEscalation: true}, CPUs: 2, MemoryMiB: 4096}); len(got) != 0 {
		t.Errorf("spec without agent: got %v, want no changes (opencode is the default)", got)
	}
	changed := vm.Spec{Agent: "claude", CPUs: 4, MemoryMiB: 2048, Docker: true, Profile: "work", GuestProtocol: guest.Protocol, Guest: &guest.Config{IPv6: true}}
	want := []string{"profile", "cpu", "memory", "enable_docker", "protect_git", "network.ipv6", "agent"}
	if got := statusRestartChanges(ws, changed); !slices.Equal(got, want) {
		t.Errorf("changed spec: got %v, want %v", got, want)
	}
	dockerWS := *ws
	dockerWS.EnableDocker = true
	dockerWS.DockerDisk = "40g"
	dockerSpec := matching
	dockerSpec.Docker = true
	if got := statusRestartChanges(&dockerWS, dockerSpec); !slices.Equal(got, []string{"docker_disk"}) {
		t.Errorf("grown disk: got %v, want [docker_disk]", got)
	}
	dockerWS.DockerDisk = "20g"
	if got := statusRestartChanges(&dockerWS, dockerSpec); len(got) != 0 {
		t.Errorf("default disk against a spec without disk_size: got %v, want no changes", got)
	}
	otherBinary := matching
	otherBinary.GuestBinary = "/other/jailoc-guest"
	if got := statusRestartChanges(ws, otherBinary); len(got) != 0 {
		t.Errorf("guest of another version with the same protocol: got %v, want no changes", got)
	}
	matching.GuestProtocol = 0
	// Guests before protocol 2 enable IPv6 whatever the spec says.
	if got := statusRestartChanges(ws, matching); !slices.Equal(got, []string{"network.ipv6", "the upgraded jailoc guest"}) {
		t.Errorf("guest with another protocol: got %v", got)
	}
	omo, ok := agent.Lookup("omo")
	if !ok {
		t.Fatal("omo harness not found")
	}
	omoWS := &workspace.Resolved{Name: "omo", Agent: omo, CPU: 2, Memory: "4g"}
	oldOmoSpec := vm.Spec{Agent: "omo", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol, Guest: &guest.Config{}}
	if got := statusRestartChanges(omoWS, oldOmoSpec); !slices.Equal(got, []string{"tmpfs mounts"}) {
		t.Errorf("omo spec without tmpfs: got %v, want [tmpfs mounts]", got)
	}
	newOmoSpec := oldOmoSpec
	newOmoSpec.Guest = &guest.Config{Tmpfs: []string{"/home/agent/.omo/agent/rpc"}}
	if got := statusRestartChanges(omoWS, newOmoSpec); len(got) != 0 {
		t.Errorf("omo spec with matching tmpfs: got %v, want no changes", got)
	}
}

func TestConfigLabel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g"}
	running := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol}
	if got := configLabel(ws, running, false); got != "up to date" {
		t.Errorf("unchanged: got %q", got)
	}
	if got, want := configLabel(ws, running, true), "changed since the VM started; `jailoc reload ws` applies it"; got != want {
		t.Errorf("changed: got %q, want %q", got, want)
	}
	running.CPUs = 1
	if got, want := configLabel(ws, running, true), "`jailoc restart ws` needed for cpu; this ends running sessions"; got != want {
		t.Errorf("restart: got %q, want %q", got, want)
	}
}

func TestStatusLabels(t *testing.T) {
	t.Parallel()
	stopped := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"never started", stoppedLabel(nil), "stopped (never started)"},
		{"killed", stoppedLabel(&vm.State{PID: 42, Ready: true}), "stopped (supervisor pid 42 exited unexpectedly)"},
		{"stopped", stoppedLabel(&vm.State{Updated: stopped}), "stopped since " + stopped.Local().Format(time.RFC3339)},
		{"uptime seconds", formatUptime(30 * time.Second), "less than a minute"},
		{"uptime minutes", formatUptime(5*time.Minute + 20*time.Second), "5m"},
		{"uptime hours", formatUptime(26*time.Hour + 3*time.Minute), "26h3m"},
		{"resources", resourcesLabel(vm.Spec{CPUs: 2, MemoryMiB: 4096}), "2 CPUs, 4096 MiB memory"},
		{"resources docker", resourcesLabel(vm.Spec{CPUs: 2, MemoryMiB: 4096, Docker: true}), "2 CPUs, 4096 MiB memory, 20 GiB Docker disk"},
		{"resources docker disk", resourcesLabel(vm.Spec{CPUs: 2, MemoryMiB: 4096, Docker: true, DiskSize: 64 << 30}), "2 CPUs, 4096 MiB memory, 64 GiB Docker disk"},
		{"sessions", sessionsLabel(2, 0), "2"},
		{"sessions attached", sessionsLabel(2, 1), "2 (1 attached)"},
		{"agent", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "claude", Package: "@anthropic-ai/claude-code", Version: "2.1.318"}, AgentInstall: true}), "claude (@anthropic-ai/claude-code@2.1.318)"},
		{"agent args", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "omo", Package: "omo-ai"}, AgentInstall: true, AgentArgs: []string{"--model", "x"}}), "omo (omo-ai@latest), args: --model x"},
		{"agent release", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "agy", Version: "1.5.0", Release: &agent.Release{Repo: "google-antigravity/antigravity-cli"}}, AgentInstall: true}), "agy (google-antigravity/antigravity-cli@1.5.0)"},
		{"agent from image", agentLabel(&workspace.Resolved{Agent: agent.Harness{Name: "opencode", Package: "@opencode/cli"}}), "opencode (installed by the image)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.got != tt.want {
				t.Errorf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}

func TestLargeImageWarning(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		layers int
		size   int64
		want   string
	}{
		{name: "small", layers: largeImageLayers, size: largeImageBytes},
		{name: "many layers", layers: 30, size: 1 << 30, want: "image img has 30 layers; converting"},
		{name: "large", layers: 10, size: 6 << 30, want: "image img has 6 GiB; converting"},
		{name: "both", layers: 25, size: 7 << 30, want: "image img has 25 layers and 7 GiB; converting"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := largeImageWarning("image img has", tt.layers, tt.size)
			if tt.want == "" {
				if got != "" {
					t.Errorf("largeImageWarning = %q, want none", got)
				}
				return
			}
			assertContains(t, got, tt.want)
		})
	}
}

func TestDockerfileBase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		chain []string
		want  string
	}{
		{name: "image used as is", chain: []string{"img"}},
		{name: "no workspace Dockerfile", chain: []string{"base", "agent", "agent"}, want: "agent"},
		{name: "workspace Dockerfile", chain: []string{"base", "docker", "agent", "overlay"}, want: "agent"},
		{name: "Dockerfile on defaults image", chain: []string{"defaults", "overlay"}, want: "defaults"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := dockerfileBase(tt.chain); got != tt.want {
				t.Errorf("dockerfileBase(%v) = %q, want %q", tt.chain, got, tt.want)
			}
		})
	}
}

func TestImportPhases(t *testing.T) {
	t.Parallel()
	start := time.Unix(1000, 0)
	p := importPhases{start: start}
	p.update(vm.ImportProgress{Phase: vm.PhaseSave, Bytes: 512}, start.Add(time.Second))
	p.update(vm.ImportProgress{Phase: vm.PhaseExtract, Total: 3}, start.Add(4*time.Second))
	p.update(vm.ImportProgress{Phase: vm.PhaseExtract, Layers: 3, Total: 3}, start.Add(9*time.Second))
	if !p.converting.Equal(start.Add(4*time.Second)) || p.bytes != 512 || p.layers != 3 {
		t.Errorf("phases = %+v, want conversion from 4s with 512 bytes and 3 layers", p)
	}
}

func TestLogsVM(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "logs"}
	stateDir := vmStateDir(ws.Name)

	var out bytes.Buffer
	if err := logsVM(context.Background(), ws, false, &out); err == nil {
		t.Fatal("logsVM succeeded without logs")
	}

	// `jailoc up` failed before the VM started.
	writeLogFile(t, upLogPath(stateDir), "up\n")
	out.Reset()
	if err := logsVM(context.Background(), ws, false, &out); err != nil || out.String() != "up\n" {
		t.Fatalf("logsVM = %q, %v; want the up log", out.String(), err)
	}

	writeLogFile(t, filepath.Join(stateDir, "supervisor.log"), "supervisor\n")
	writeLogFile(t, filepath.Join(stateDir, "microvm", "console.log.2"), "console2\n")
	writeLogFile(t, filepath.Join(stateDir, "microvm", "console.log.1"), "console1\n")
	writeLogFile(t, filepath.Join(stateDir, "microvm", "console.log"), "console\n")
	out.Reset()
	if err := logsVM(context.Background(), ws, false, &out); err != nil || out.String() != "up\nsupervisor\nconsole2\nconsole1\nconsole\n" {
		t.Fatalf("logsVM = %q, %v; want the up, supervisor and rotated console logs in order", out.String(), err)
	}
}

func writeLogFile(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunUpVMLogsFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{
		Name:    "fails",
		Env:     []string{"TOKEN=x"},
		Secrets: []workspace.ResolvedSecret{{Name: "TOKEN", Kind: workspace.SecretKindEnv}},
	}
	if err := runUpVM(context.Background(), &config.Config{}, ws); err == nil {
		t.Fatal("runUpVM succeeded with a colliding secret")
	}
	data, err := os.ReadFile(upLogPath(vmStateDir(ws.Name)))
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, string(data), `msg="up failed" workspace=fails error="validate secrets for workspace \"fails\": env secret \"TOKEN\" collides`)
}

func TestLogsVMFollowsUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "follow"}
	stateDir := vmStateDir(ws.Name)
	upLog := upLogPath(stateDir)
	supervisor := filepath.Join(stateDir, "supervisor.log")
	console := filepath.Join(stateDir, "microvm", "console.log")
	// The logs of a VM that ran before.
	writeLogFile(t, upLog, "old up records\n")
	writeLogFile(t, supervisor, "old supervisor records\n")
	writeLogFile(t, console, "old console output\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- logsVM(ctx, ws, true, out) }()

	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for strings.Count(out.String(), want) != 1 {
			if time.Now().After(deadline) {
				t.Fatalf("output %q does not hold %q once", out.String(), want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	appendLog := func(name, data string) {
		t.Helper()
		f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: test temp dir
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(data); err != nil {
			t.Fatal(err)
		}
	}
	waitFor("old up records\nold supervisor records\nold console output\n")

	// The next `jailoc up` truncates its log, builds the image and starts
	// the VM, which starts its logs afresh, and then logs more steps.
	writeLogFile(t, upLog, "resolve\n")
	waitFor("resolve\n")
	appendLog(upLog, "build\n")
	waitFor("resolve\nbuild\n")
	writeLogFile(t, supervisor, "vm started\n")
	writeLogFile(t, console, "boot\n")
	waitFor("vm started\n")
	waitFor("boot\n")
	appendLog(upLog, "deliver secrets\n")
	appendLog(console, "ready\n")
	// Lines of different logs interleave in the order they are polled.
	waitFor("deliver secrets\n")
	waitFor("ready\n")
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("logsVM: %v", err)
	}
}

// syncBuffer is a bytes.Buffer that one goroutine writes while another
// reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWorkspaceSpecTmpfs(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	omo, ok := agent.Lookup("omo")
	if !ok {
		t.Fatal("omo harness not found")
	}
	claude, ok := agent.Lookup("claude")
	if !ok {
		t.Fatal("claude harness not found")
	}

	tests := []struct {
		name  string
		agent agent.Harness
		want  []string
	}{
		{"omo", omo, []string{"/home/agent/.omo/agent/rpc"}},
		{"claude", claude, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := &workspace.Resolved{
				Name:       "test-" + tt.name,
				Agent:      tt.agent,
				CPU:        2,
				Memory:     "4g",
				DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g",
			}
			spec, err := workspaceSpec(context.Background(), ws, vmRuntimeFiles{}, "img:latest", nil, nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if !slices.Equal(spec.Guest.Tmpfs, tt.want) {
				t.Fatalf("Guest.Tmpfs = %q, want %q", spec.Guest.Tmpfs, tt.want)
			}
		})
	}
}

func TestWorkspaceSpecProxy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		proxy     []workspace.ProxyRoute
		wantPorts []uint16 // nil: a relay that allows no port
	}{
		{name: "none"},
		{
			name:      "proxy routes",
			proxy:     []workspace.ProxyRoute{{Name: "api", Port: 8080}, {Name: "app", Port: 3000}},
			wantPorts: []uint16{3000, 8080},
		},
		{
			name:      "proxy routes with a shared port",
			proxy:     []workspace.ProxyRoute{{Name: "a", Port: 3000}, {Name: "b", Port: 3000}, {Name: "c", Port: 6080}},
			wantPorts: []uint16{3000, 6080},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "test", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g", Proxy: tt.proxy}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			// Every workspace VM runs the relay, so a reload can add routes.
			if spec.Guest.ProxyRelay == nil || !slices.Equal(spec.Guest.ProxyRelay.Ports, tt.wantPorts) {
				t.Errorf("Guest.ProxyRelay = %+v, want ports %v", spec.Guest.ProxyRelay, tt.wantPorts)
			}
		})
	}
}

func TestStatusRestartChangesProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol}
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g"}
	tests := []struct {
		name    string
		proxy   []workspace.ProxyRoute
		running guest.Config
		// want is what a guest that cannot replace its relay allowlist
		// needs a restart for; a guest that can needs none (a reload applies
		// the change).
		want []string
	}{
		{name: "none"},
		{name: "proxy added", proxy: []workspace.ProxyRoute{{Name: "app", Port: 3000}}, want: []string{"proxy"}},
		{
			name: "proxy port changed", proxy: []workspace.ProxyRoute{{Name: "app", Port: 3001}},
			running: guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}}, want: []string{"proxy"},
		},
		{
			name: "proxy renamed on the same port", proxy: []workspace.ProxyRoute{{Name: "other", Port: 3000}},
			running: guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}},
		},
		{
			name: "proxy removed", running: guest.Config{ProxyRelay: &guest.ProxyRelay{Ports: []uint16{3000}}}, want: []string{"proxy"},
		},
	}
	for _, tt := range tests {
		w := *ws
		w.Proxy = tt.proxy
		running := base
		g := tt.running
		running.Guest = &g
		if got := statusRestartChanges(&w, running); len(got) != 0 {
			t.Errorf("%s: live proxy guest: got %v, want no restart", tt.name, got)
		}
		old := running
		old.GuestProtocol = liveProxyGuestProtocol - 1
		wantOld := append(slices.Clone(tt.want), "the upgraded jailoc guest")
		if got := statusRestartChanges(&w, old); !slices.Equal(got, wantOld) {
			t.Errorf("%s: old guest: got %v, want %v", tt.name, got, wantOld)
		}
	}
}

func TestDeliverSecretsRegistersConsoleRedactionFirst(t *testing.T) {
	t.Parallel()
	// Unix socket paths are limited to about 100 bytes, so the state dir
	// lives directly under /tmp.
	stateDir, err := os.MkdirTemp("/tmp", "jlc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	ln, err := net.Listen("unix", filepath.Join(stateDir, "console.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		var vals []string
		if err := json.NewDecoder(c).Decode(&vals); err != nil {
			return
		}
		got <- vals
		_, _ = io.WriteString(c, "ok\n")
	}()

	payload := guest.Secrets{Env: []string{"API_TOKEN=delivered-secret-value"}}
	// The state dir has no SSH keys, so delivery to the guest fails after the
	// supervisor has received the values.
	if err := deliverSecrets(context.Background(), stateDir, payload); err == nil {
		t.Fatal("deliverSecrets() = nil, want SSH error")
	}
	select {
	case vals := <-got:
		if !slices.Contains(vals, "delivered-secret-value") {
			t.Errorf("console redactor got %q, want delivered-secret-value", vals)
		}
	default:
		t.Fatal("deliverSecrets did not register secret values for console redaction")
	}
}

func TestDeliverSecretsFailsWhenConsoleRedactionRejects(t *testing.T) {
	t.Parallel()
	stateDir, err := os.MkdirTemp("/tmp", "jlc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateDir) })
	ln, err := net.Listen("unix", filepath.Join(stateDir, "console.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_ = c.Close()
	}()

	payload := guest.Secrets{Env: []string{"API_TOKEN=delivered-secret-value"}}
	err = deliverSecrets(context.Background(), stateDir, payload)
	if err == nil || !strings.Contains(err.Error(), "console redaction") {
		t.Fatalf("deliverSecrets() = %v, want console redaction error", err)
	}
}

func TestLogTailFollowsDoubleRotation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := filepath.Join(dir, "console.log")
	backups := []string{base + ".2", base + ".1"}
	rotate := func(next string) {
		t.Helper()
		_ = os.Remove(backups[0])
		if err := os.Rename(backups[1], backups[0]); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if err := os.Rename(base, backups[1]); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(base, []byte(next), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(base, []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := &logTail{path: base, backups: backups}
	t.Cleanup(tail.close)
	var out bytes.Buffer
	if err := tail.copy(&out); err != nil {
		t.Fatal(err)
	}

	f, err := os.OpenFile(base, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // G304: test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("a2\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	rotate("b\n")
	rotate("c\n")
	if err := tail.copy(&out); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "a\na2\nb\nc\n"; got != want {
		t.Fatalf("followed output = %q, want %q", got, want)
	}
}

func TestWorkspaceSpecStateAndCacheDisks(t *testing.T) {
	t.Parallel()
	oc, _ := agent.Lookup("opencode1")
	ephemeral := config.EphemeralProfile()
	tests := []struct {
		name          string
		profile       *config.Profile
		shares        []vm.Share
		wantState     bool
		wantDirs      []string
		wantEphemeral bool
		wantCache     bool
	}{
		{name: "defaults", wantState: true, wantDirs: []string{"opencode-data", "opencode-state", "opencode1-omo"}, wantCache: true},
		{name: "ephemeral profile", profile: &ephemeral, wantState: true, wantDirs: []string{"opencode-data", "opencode-state", "opencode1-omo"}, wantEphemeral: true, wantCache: true},
		{name: "cache share", shares: []vm.Share{{GuestPath: guestUserCache}}, wantState: true, wantDirs: []string{"opencode-data", "opencode-state", "opencode1-omo"}},
		{name: "every directory shared", shares: func() []vm.Share {
			out := []vm.Share{{GuestPath: guestUserCache}}
			for _, d := range oc.State {
				out = append(out, vm.Share{GuestPath: d.Path})
			}
			return out
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "test", Agent: oc, AgentProfile: tt.profile, CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "12g", CacheDisk: "30g"}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", tt.shares, nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if spec.StateDisk != tt.wantState || spec.CacheDisk != tt.wantCache {
				t.Fatalf("StateDisk, CacheDisk = %v, %v, want %v, %v", spec.StateDisk, spec.CacheDisk, tt.wantState, tt.wantCache)
			}
			if tt.wantState {
				if spec.StateDiskSize != 12<<30 {
					t.Errorf("StateDiskSize = %d, want 12 GiB", spec.StateDiskSize)
				}
				st := spec.Guest.State
				if st == nil || st.Agent != oc.Name || !slices.Equal(st.Dirs, tt.wantDirs) || st.Ephemeral != tt.wantEphemeral {
					t.Errorf("Guest.State = %+v, want agent %s dirs %v ephemeral %v", st, oc.Name, tt.wantDirs, tt.wantEphemeral)
				}
			} else if spec.StateDiskSize != 0 || spec.Guest.State != nil {
				t.Errorf("no state disk, got size %d and Guest.State %+v", spec.StateDiskSize, spec.Guest.State)
			}
			if tt.wantCache {
				if spec.CacheDiskSize != 30<<30 || spec.Guest.Cache == nil {
					t.Errorf("CacheDiskSize = %d, Guest.Cache = %+v, want 30 GiB and a cache disk", spec.CacheDiskSize, spec.Guest.Cache)
				}
			} else if spec.CacheDiskSize != 0 || spec.Guest.Cache != nil {
				t.Errorf("no cache disk, got size %d and Guest.Cache %+v", spec.CacheDiskSize, spec.Guest.Cache)
			}
		})
	}

	t.Run("custom agent and alias use the built-in harness", func(t *testing.T) {
		t.Parallel()
		custom, _ := agent.Lookup("claude")
		custom.Name = "claude-opus" // as workspace.Resolve derives a custom agent
		alias, _ := agent.Lookup("opencode")
		for h, want := range map[*agent.Harness]string{&custom: "claude", &alias: "opencode2"} {
			ws := &workspace.Resolved{Name: "test", Agent: *h, CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g"}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if st := spec.Guest.State; st == nil || st.Agent != want {
				t.Errorf("agent %s: Guest.State = %+v, want agent %s", h.Name, st, want)
			}
		}
	})

	for _, bad := range []workspace.Resolved{
		{Name: "t", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "1m", CacheDisk: "20g"},
		{Name: "t", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "x"},
	} {
		if _, err := workspaceSpec(t.Context(), &bad, vmRuntimeFiles{}, "image", nil, nil, nil); err == nil {
			t.Errorf("workspaceSpec accepted state_disk %q cache_disk %q", bad.StateDisk, bad.CacheDisk)
		}
	}
}

func TestStatusRestartChangesStateAndCacheDisk(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g", StateDisk: "10g", CacheDisk: "20g"}
	running := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol, Guest: &guest.Config{}, StateDisk: true, CacheDisk: true}
	if got := statusRestartChanges(ws, running); len(got) != 0 {
		t.Fatalf("defaults against a spec without sizes: got %v, want none", got)
	}
	ws.StateDisk, ws.CacheDisk = "20g", "40g"
	if got := statusRestartChanges(ws, running); !slices.Equal(got, []string{"state_disk", "cache_disk"}) {
		t.Fatalf("grown disks: got %v, want [state_disk cache_disk]", got)
	}
	running.StateDisk, running.CacheDisk = false, false
	if got := statusRestartChanges(ws, running); len(got) != 0 {
		t.Fatalf("spec without disks: got %v, want none", got)
	}
}

func TestWorkspaceSpecBuildCA(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ws := &workspace.Resolved{Name: "ws", CPU: 2, Memory: "2G", DockerDisk: "20g", StateDisk: "10g", CacheDisk: "20g"}
	ca, err := docker.NewBuildCA([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	tests := []struct {
		name string
		ca   *docker.BuildCA
		want string
	}{
		{name: "no bundle", want: "none"},
		{name: "bundle", ca: ca, want: "sha256:" + ca.Digest()},
	}
	for _, tt := range tests {
		spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil, tt.ca)
		if err != nil {
			t.Fatalf("%s: workspaceSpec: %v", tt.name, err)
		}
		if spec.BuildCA != tt.want {
			t.Errorf("%s: BuildCA = %q, want %q", tt.name, spec.BuildCA, tt.want)
		}
	}
}

func TestBuildWorkspaceImageCAForwardingFollowsImageStrategy(t *testing.T) {
	t.Parallel()
	ca, err := docker.NewBuildCA([]byte("bundle"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	cfg := &config.Config{}
	direct := &workspace.Resolved{Name: "ws", Image: "custom:latest"}
	built := &workspace.Resolved{Name: "ws", EnableDocker: true}
	if buildsImage(cfg, direct) {
		t.Fatal("buildsImage(direct image) = true, want false")
	}
	if !buildsImage(cfg, built) {
		t.Fatal("buildsImage(cascade image) = false, want true")
	}
	for _, tt := range []struct {
		name    string
		ws      *workspace.Resolved
		builder string
		want    bool
		wantErr bool
	}{
		{name: "direct image with docker builder", ws: direct, builder: config.BuilderDocker, want: true},
		{name: "direct image with auto uses host", ws: direct, builder: config.BuilderAuto, want: true},
		{name: "built image with docker builder is rejected", ws: built, builder: config.BuilderDocker, wantErr: true},
		{name: "built image with auto uses builder vm", ws: built, builder: config.BuilderAuto},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			forwardCA := buildsImage(cfg, tt.ws) && ca != nil
			got, err := useHostDocker(tt.builder, tt.ws.IsolatedBuild, forwardCA, nil)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("useHostDocker = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
