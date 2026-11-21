//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
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
		name     string
		builder  string
		isolated bool
		hostErr  error
		want     bool
		wantErr  bool
	}{
		{name: "auto default uses host", builder: "", want: true},
		{name: "auto uses host", builder: config.BuilderAuto, want: true},
		{name: "auto falls back to vm", builder: config.BuilderAuto, hostErr: unusable},
		{name: "docker uses host", builder: config.BuilderDocker, want: true},
		{name: "docker without host fails", builder: config.BuilderDocker, hostErr: unusable, wantErr: true},
		{name: "vm never uses host", builder: config.BuilderVM},
		{name: "isolated build uses its vm", builder: config.BuilderDocker, isolated: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := useHostDocker(tt.builder, tt.isolated, tt.hostErr)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("useHostDocker = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
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
			ws := &workspace.Resolved{Name: "test", Paths: tt.paths, CPU: 2, Memory: "2G", DockerDisk: "20g"}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil)
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
			absent:   append([]string{"NODE_USE_SYSTEM_CA"}, caBundleVars...),
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
	writeLogFile(t, filepath.Join(stateDir, "microvm", "console.log"), "console\n")
	out.Reset()
	if err := logsVM(context.Background(), ws, false, &out); err != nil || out.String() != "up\nsupervisor\nconsole\n" {
		t.Fatalf("logsVM = %q, %v; want the up, supervisor and console logs in order", out.String(), err)
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

func TestWorkspaceSpecWeb(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		web       []workspace.WebRoute
		wantPorts []uint16 // nil: no web relay
	}{
		{name: "none"},
		{
			name:      "web routes",
			web:       []workspace.WebRoute{{Name: "api", Port: 8080}, {Name: "app", Port: 3000}},
			wantPorts: []uint16{3000, 8080},
		},
		{
			name:      "web routes with a shared port",
			web:       []workspace.WebRoute{{Name: "a", Port: 3000}, {Name: "b", Port: 3000}, {Name: "c", Port: 6080}},
			wantPorts: []uint16{3000, 6080},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ws := &workspace.Resolved{Name: "test", CPU: 2, Memory: "2G", DockerDisk: "20g", Web: tt.web}
			spec, err := workspaceSpec(t.Context(), ws, vmRuntimeFiles{}, "image", nil, nil)
			if err != nil {
				t.Fatalf("workspaceSpec: %v", err)
			}
			if tt.wantPorts == nil {
				if spec.Guest.WebRelay != nil {
					t.Errorf("Guest.WebRelay = %+v, want nil", spec.Guest.WebRelay)
				}
			} else if spec.Guest.WebRelay == nil || !slices.Equal(spec.Guest.WebRelay.Ports, tt.wantPorts) {
				t.Errorf("Guest.WebRelay = %+v, want ports %v", spec.Guest.WebRelay, tt.wantPorts)
			}
		})
	}
}

func TestStatusRestartChangesWeb(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	base := vm.Spec{Agent: "opencode", CPUs: 2, MemoryMiB: 4096, GuestProtocol: guest.Protocol}
	ws := &workspace.Resolved{Name: "ws", Agent: agent.Harness{Name: "opencode"}, CPU: 2, Memory: "4g"}
	tests := []struct {
		name    string
		web     []workspace.WebRoute
		running guest.Config
		want    []string
	}{
		{name: "none"},
		{name: "web added", web: []workspace.WebRoute{{Name: "app", Port: 3000}}, want: []string{"web"}},
		{
			name: "web port changed", web: []workspace.WebRoute{{Name: "app", Port: 3001}},
			running: guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{3000}}}, want: []string{"web"},
		},
		{
			name: "web renamed on the same port", web: []workspace.WebRoute{{Name: "other", Port: 3000}},
			running: guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{3000}}},
		},
		{
			name: "web removed", running: guest.Config{WebRelay: &guest.WebRelay{Ports: []uint16{3000}}}, want: []string{"web"},
		},
	}
	for _, tt := range tests {
		w := *ws
		w.Web = tt.web
		running := base
		g := tt.running
		running.Guest = &g
		if got := statusRestartChanges(&w, running); !slices.Equal(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}
}
