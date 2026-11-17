//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/guest"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestVMMemoryMiB(t *testing.T) {
	t.Parallel()
	tests := []struct {
		limit   string
		want    uint32
		wantErr bool
	}{
		{limit: "4g", want: 4096},
		{limit: "512m", want: 512},
		{limit: "1536M", want: 1536},
		{limit: "1k", wantErr: true},
		{limit: "511m", wantErr: true},
		{limit: "1073741824", want: 1024},
		{limit: "", wantErr: true},
		{limit: "0g", wantErr: true},
		{limit: "g", wantErr: true},
		{limit: "99999999g", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.limit, func(t *testing.T) {
			t.Parallel()
			got, err := vmMemoryMiB(tt.limit)
			if (err != nil) != tt.wantErr {
				t.Fatalf("vmMemoryMiB(%q) error = %v, wantErr %v", tt.limit, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("vmMemoryMiB(%q) = %d, want %d", tt.limit, got, tt.want)
			}
		})
	}
}

func TestVMDockerDiskSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		size    string
		want    int64
		wantErr bool
	}{
		{size: "20g", want: 20 << 30},
		{size: "100G", want: 100 << 30},
		{size: "1024m", want: 1 << 30},
		{size: "1048577k", want: 1<<30 + 1<<20},
		{size: "1073741824", want: 1 << 30},
		{size: "1023m", wantErr: true},
		{size: "", wantErr: true},
		{size: "0g", wantErr: true},
		{size: "g", wantErr: true},
		{size: "9999999999999g", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.size, func(t *testing.T) {
			t.Parallel()
			got, err := vmDockerDiskSize(tt.size)
			if (err != nil) != tt.wantErr {
				t.Fatalf("vmDockerDiskSize(%q) error = %v, wantErr %v", tt.size, err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("vmDockerDiskSize(%q) = %d, want %d", tt.size, got, tt.want)
			}
		})
	}
}

func TestVMPolicy(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{
		Name:          "w",
		DNSUpstream:   "9.9.9.9",
		InternalZones: []string{"internal.example.com"},
		NetworkRules: []config.NetworkRule{
			{Name: "no-paste", Deny: []string{"pastebin.com", "*.ngrok.io"}},
			{Name: "db", Allow: []string{"10.1.0.5/16", "192.168.1.10"}, Proto: "tcp", Ports: []config.RulePort{" 5432 ", "8000 - 8100"}},
			{Name: "dev-deps", Allow: []string{"github.com", "registry.npmjs.org"}},
			{Name: "agent", Allow: []string{"api.anthropic.com"}, Proto: "tcp", Ports: []config.RulePort{"443"}},
		},
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	var got struct {
		DNS           string   `json:"dns"`
		Deny          []vmRule `json:"deny"`
		Allow         []vmRule `json:"allow"`
		InternalZones []string `json:"internal_zones"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if !slices.Equal(got.InternalZones, []string{"internal.example.com"}) {
		t.Fatalf("internal_zones = %v, want [internal.example.com]", got.InternalZones)
	}
	if got.DNS != "9.9.9.9" {
		t.Fatalf("dns = %q, want 9.9.9.9", got.DNS)
	}
	if len(got.Deny) != 1 || got.Deny[0].Name != "no-paste" || !slices.Equal(got.Deny[0].Names, []string{"pastebin.com", "*.ngrok.io"}) {
		t.Fatalf("deny = %+v", got.Deny)
	}
	if len(got.Allow) != 3 {
		t.Fatalf("allow = %+v, want 3 rules", got.Allow)
	}
	if !slices.Equal(got.Allow[0].CIDRs, []string{"10.1.0.0/16", "192.168.1.10/32"}) || !slices.Equal(got.Allow[0].Ports, []string{"5432", "8000-8100"}) {
		t.Fatalf("allow[0] = %+v", got.Allow[0])
	}
	if got.Allow[1].Name != "dev-deps" || !slices.Contains(got.Allow[1].Names, "registry.npmjs.org") {
		t.Fatalf("allow[1] = %+v", got.Allow[1])
	}
	if got.Allow[2].Name != "agent" || !slices.Equal(got.Allow[2].Names, []string{"api.anthropic.com"}) {
		t.Fatalf("allow[2] = %+v", got.Allow[2])
	}
}

func TestVMPolicyLegacyFallback(t *testing.T) {
	t.Parallel()
	ws := &workspace.Resolved{
		Name:            "w",
		DNSUpstream:     "10.0.0.53",
		AllowedNetworks: []string{"10.1.0.0/16", "fd00::/8"},
		AllowedHosts:    []string{"192.168.1.10", "fd12::7"},
		DNSBlockedZones: []string{"corp.example"},
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	var got struct {
		DNS   string   `json:"dns"`
		Deny  []vmRule `json:"deny"`
		Allow []vmRule `json:"allow"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if got.DNS != "10.0.0.53" || len(got.Deny) != 1 || !slices.Equal(got.Deny[0].Names, []string{"corp.example", "*.corp.example"}) {
		t.Fatalf("legacy policy deny/dns = %s", data)
	}
	if len(got.Allow) != 2 || !slices.Equal(got.Allow[0].CIDRs, []string{"192.168.1.10/32", "fd12::7/128"}) {
		t.Fatalf("legacy policy allow = %+v", got.Allow)
	}
}

// fakeResolver answers legacy host lookups from maps; a missing name fails.
type fakeResolver struct {
	ips    map[string]string
	cnames map[string]string
	looked []string
}

func (r *fakeResolver) LookupIP(_ context.Context, _, host string) ([]net.IP, error) {
	r.looked = append(r.looked, host)
	if ip, ok := r.ips[host]; ok {
		return []net.IP{net.ParseIP(ip)}, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

func (r *fakeResolver) LookupCNAME(_ context.Context, host string) (string, error) {
	if c, ok := r.cnames[host]; ok {
		return c, nil
	}
	if _, ok := r.ips[host]; ok {
		return host + ".", nil
	}
	return "", fmt.Errorf("no such host %s", host)
}

// TestVMPolicyLegacyHostsOpenNoResolvedCIDRs checks that legacy
// allowed_hosts names never become CIDR rules from host resolution, even when
// an internal-looking name is an alias of a rebinding name: only explicit IPs
// become CIDRs. Not parallel: it replaces hostResolver.
func TestVMPolicyLegacyHostsOpenNoResolvedCIDRs(t *testing.T) {
	fake := &fakeResolver{
		ips:    map[string]string{"10-0-0-5.nip.io": "10.0.0.5", "dev.corp.example": "10.0.0.5"},
		cnames: map[string]string{"dev.corp.example": "10-0-0-5.nip.io."},
	}
	orig := hostResolver
	hostResolver = fake
	t.Cleanup(func() { hostResolver = orig })
	ws := &workspace.Resolved{
		Name:          "w",
		AllowedHosts:  []string{"10-0-0-5.nip.io", "dev.corp.example", "10.9.9.9"},
		InternalZones: []string{"dev.corp.example"},
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	var got struct {
		Allow         []vmRule `json:"allow"`
		InternalZones []string `json:"internal_zones"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	var cidrs []string
	for _, r := range got.Allow {
		cidrs = append(cidrs, r.CIDRs...)
	}
	if !slices.Equal(cidrs, []string{"10.9.9.9/32"}) {
		t.Fatalf("allow CIDRs = %v, want only the explicit IP", cidrs)
	}
	if !slices.Equal(got.InternalZones, []string{"dev.corp.example"}) {
		t.Fatalf("internal_zones = %v, want [dev.corp.example]", got.InternalZones)
	}
}

func TestLegacyAliasWarnings(t *testing.T) {
	t.Parallel()
	r := &fakeResolver{
		ips: map[string]string{
			"db.corp.example":      "10.0.0.5",
			"wiki.corp.example":    "10.0.0.6",
			"git.corp.example":     "10.0.0.7",
			"cdn.example.com":      "93.184.216.34",
			"plain.corp.example":   "10.0.0.8",
			"api.lab.corp.example": "10.0.0.9",
		},
		cnames: map[string]string{
			"db.corp.example":      "db-node.corp.example.",
			"wiki.corp.example":    "wiki.other.example.",
			"git.corp.example":     "git.internal.example.",
			"cdn.example.com":      "edge.cdn.example.net.",
			"api.lab.corp.example": "api.lab.corp.example.",
		},
	}
	hosts := []string{"db.corp.example", "wiki.corp.example", "git.corp.example", "cdn.example.com", "plain.corp.example", "api.lab.corp.example", "10.1.2.3", "*.wild.example", "missing.corp.example"}
	got := legacyAliasWarnings(context.Background(), r, hosts, []string{"internal.example"})
	if len(got) != 2 || !strings.Contains(got[0], `"db.corp.example" is an alias of "db-node.corp.example"`) || !strings.Contains(got[1], `"wiki.corp.example" is an alias of "wiki.other.example"`) {
		t.Fatalf("warnings = %q, want db.corp.example and wiki.corp.example", got)
	}
	if got := legacyAliasWarnings(context.Background(), r, hosts, []string{"corp.example", "other.example", "internal.example"}); len(got) != 0 {
		t.Fatalf("warnings with the zones trusted = %q, want none", got)
	}
}

func TestVMPolicyIPv6(t *testing.T) {
	t.Parallel()
	for _, enabled := range []bool{false, true} {
		data, err := vmPolicy(context.Background(), &workspace.Resolved{Name: "w", IPv6: enabled})
		if err != nil {
			t.Fatalf("vmPolicy: %v", err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal %s: %v", data, err)
		}
		v, ok := got["ipv6"]
		// Leaving the key out when IPv6 is off keeps the policy readable by brokers that predate it.
		if enabled != ok || (ok && string(v) != "true") {
			t.Fatalf("ipv6=%v: policy = %s", enabled, data)
		}
	}
}

func TestVMSharesLimit(t *testing.T) {
	t.Parallel()
	oc, _ := agent.Lookup("opencode1")
	for _, docker := range []bool{false, true} {
		limit := vm.MaxShares(docker)
		paths := make([]string, limit+1)
		for i := range paths {
			paths[i] = fmt.Sprintf("/src/p%02d", i)
		}
		ws := &workspace.Resolved{Name: "w", Paths: paths[:limit], Agent: oc, EnableDocker: docker}
		if _, err := vmShares(ws); err != nil {
			t.Fatalf("docker=%v: %d shares: %v", docker, limit, err)
		}
		ws.Paths = paths
		_, err := vmShares(ws)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceed the limit of %d", limit)) {
			t.Fatalf("docker=%v: %d shares: err = %v, want limit error", docker, limit+1, err)
		}
	}
}

func TestVMShares(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "project")
	tools := filepath.Join(home, "tools")
	file := filepath.Join(home, "file.txt")
	for _, d := range []string{project, tools} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	oc, _ := agent.Lookup("opencode1")
	guestOpenCodeDir, guestOpenCodeState := oc.State[0].Path, oc.State[1].Path
	cacheDir := filepath.Join(home, ".cache", "jailoc", "w")
	ws := &workspace.Resolved{
		Name:  "w",
		Paths: []string{project},
		Agent: oc,
		Mounts: []string{
			tools + ":/opt/tools:ro",
			project + ":" + project + ":ro",
			"~/created:/opt/created",
			"~/data:" + guestOpenCodeDir,
		},
	}
	shares, err := vmShares(ws)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	want := []vm.Share{
		{HostPath: filepath.Join(home, "data"), GuestPath: guestOpenCodeDir},
		{HostPath: filepath.Join(home, "created"), GuestPath: "/opt/created"},
		{HostPath: tools, GuestPath: "/opt/tools", ReadOnly: true},
		{HostPath: project, GuestPath: project},
	}
	slices.SortFunc(want, func(a, b vm.Share) int { return strings.Compare(a.GuestPath, b.GuestPath) })
	if !slices.Equal(shares, want) {
		t.Fatalf("shares =\n%+v\nwant\n%+v", shares, want)
	}
	if info, err := os.Stat(filepath.Join(home, "created")); err != nil || !info.IsDir() {
		t.Fatalf("mount source not created: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(cacheDir, "tui-plugin"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "tui-container.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	binds, err := vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds := []guest.Bind{
		{Source: guest.ReadOnlyHostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/opencode-state", Target: guestOpenCodeState},
		{Source: guest.HostDir + "/home/opencode1-omo", Target: "/home/agent/.omo"},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}
	data := vm.GuestDataDir(vmStateDir("w"))
	if b, err := os.ReadFile(filepath.Join(vm.GuestReadOnlyDir(vmStateDir("w")), "cache", "tui-container.json")); err != nil || string(b) != "{}" { //nolint:gosec // G304: test file
		t.Fatalf("cache not copied: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(data, "home", "opencode-data")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("opencode-data created although a mount covers it: %v", err)
	}

	ws.Agent, _ = agent.Lookup("claude")
	binds, err = vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds = []guest.Bind{
		{Source: guest.ReadOnlyHostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/claude", Target: "/home/agent/.claude"},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("claude binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}
	if b, err := os.ReadFile(filepath.Join(data, "home", "claude", ".claude.json")); err != nil || !strings.Contains(string(b), `"bypassPermissionsModeAccepted": true`) { //nolint:gosec // G304: test file
		t.Fatalf("claude global config = %q, %v", b, err)
	}

	ws.Agent, _ = agent.Lookup("copilot")
	settings := filepath.Join(data, "home", "copilot", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settings, []byte(`{"model":"m"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	var got map[string]any
	if b, err := os.ReadFile(settings); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
		t.Fatalf("read copilot settings: %q %v", b, err)
	}
	line, _ := got["statusLine"].(map[string]any)
	if got["model"] != "m" || got["storeTokenPlaintext"] != true || line["command"] != statusLineCommand("copilot") {
		t.Fatalf("copilot settings = %v", got)
	}
	if err := os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"mine"},"storeTokenPlaintext":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	got = nil
	if b, err := os.ReadFile(settings); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
		t.Fatalf("read copilot settings: %q %v", b, err)
	}
	if line, _ := got["statusLine"].(map[string]any); line["command"] != "mine" || got["storeTokenPlaintext"] != false {
		t.Fatalf("user copilot settings replaced: %v", got)
	}
	// An existing status line without storeTokenPlaintext still gets storeTokenPlaintext = true.
	if err := os.WriteFile(settings, []byte(`{"statusLine":{"type":"command","command":"mine"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	got = nil
	if b, err := os.ReadFile(settings); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
		t.Fatalf("read copilot settings: %q %v", b, err)
	}
	if line, _ := got["statusLine"].(map[string]any); line["command"] != "mine" || got["storeTokenPlaintext"] != true {
		t.Fatalf("copilot settings after adding storeTokenPlaintext = %v", got)
	}
	// The script status line of an older jailoc gives way to the current one.
	legacy, _ := json.Marshal(map[string]any{"statusLine": map[string]string{"type": "command", "command": scriptStatusLineCommand("copilot")}})
	if err := os.WriteFile(settings, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	got = nil
	if b, err := os.ReadFile(settings); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
		t.Fatalf("read copilot settings: %q %v", b, err)
	}
	if line, _ := got["statusLine"].(map[string]any); line["command"] != workspaceStatusLineCommand {
		t.Fatalf("legacy status line kept: %v", got)
	}

	// Antigravity CLI keeps its settings in a subdirectory, which a new
	// state directory doesn't have yet.
	ws.Agent, _ = agent.Lookup("agy")
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	got = nil
	agySettings := filepath.Join(data, "home", "antigravity", "antigravity-cli", "settings.json")
	if b, err := os.ReadFile(agySettings); err != nil || json.Unmarshal(b, &got) != nil { //nolint:gosec // G304: test file
		t.Fatalf("read agy settings: %q %v", b, err)
	}
	if line, _ := got["statusLine"].(map[string]any); line["command"] != statusLineCommand("agy") || got["storeTokenPlaintext"] != nil {
		t.Fatalf("agy settings = %v", got)
	}

	ws.Mounts = []string{file + ":/opt/file.txt:ro"}
	if _, err := vmShares(ws); err == nil {
		t.Fatal("vmShares accepted a file mount")
	}

	// A missing read-only source would share an empty directory.
	missing := filepath.Join(home, "missing")
	ws.Mounts = []string{missing + ":/opt/missing:ro"}
	if _, err := vmShares(ws); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("vmShares with a missing read-only source: err = %v, want does not exist", err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing read-only source created: %v", err)
	}
	// The built-in ~/.agents mount is read-only, but its missing source is
	// still created so a host without ~/.agents boots.
	ws.Mounts = []string{filepath.Join(home, ".agents") + ":/home/agent/.agents:ro"}
	if _, err := vmShares(ws); err != nil {
		t.Fatalf("vmShares with a missing default mount source: %v", err)
	}
	if info, err := os.Stat(filepath.Join(home, ".agents")); err != nil || !info.IsDir() {
		t.Fatalf("default mount source not created: %v", err)
	}
}

func TestVMAgentProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	oc, _ := agent.Lookup("opencode1")
	dataDir, stateDir := oc.State[0].Path, oc.State[1].Path
	cacheDir := filepath.Join(home, ".cache", "jailoc", "w")
	profile := config.NamedProfile("work")
	ws := &workspace.Resolved{
		Name:         "w",
		Agent:        oc,
		AgentProfile: &profile,
		Mounts:       []string{"~/data:" + dataDir},
	}

	shares, err := vmShares(ws)
	if err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	profileDir := filepath.Join(home, ".local", "share", "jailoc", "profiles", "work")
	want := []vm.Share{
		{HostPath: filepath.Join(home, "data"), GuestPath: dataDir},
		{HostPath: filepath.Join(profileDir, "opencode-state"), GuestPath: stateDir},
		{HostPath: filepath.Join(profileDir, "opencode1-omo"), GuestPath: oc.State[2].Path},
	}
	if !slices.Equal(shares, want) {
		t.Fatalf("shares =\n%+v\nwant\n%+v", shares, want)
	}
	binds, err := vmGuestData(ws, cacheDir, shares, true)
	if err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds := []guest.Bind{
		{Source: guest.ReadOnlyHostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("named profile binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}

	// Claude Code's defaults go into the profile on start only: on reload
	// the agent may be writing the file.
	ws.Agent, _ = agent.Lookup("claude")
	claudeJSON := filepath.Join(profileDir, "claude", ".claude.json")
	if shares, err = vmShares(ws); err != nil {
		t.Fatalf("vmShares: %v", err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, false); err != nil {
		t.Fatalf("vmGuestData reload: %v", err)
	}
	if _, err := os.Stat(claudeJSON); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reload wrote %s: %v", claudeJSON, err)
	}
	if _, err := vmGuestData(ws, cacheDir, shares, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	if b, err := os.ReadFile(claudeJSON); err != nil || !strings.Contains(string(b), `"bypassPermissionsModeAccepted": true`) { //nolint:gosec // G304: test file
		t.Fatalf("claude profile global config = %q, %v", b, err)
	}
	ws.Agent = oc

	ephemeral := config.EphemeralProfile()
	ws.AgentProfile = &ephemeral
	ws.Mounts = nil
	if shares, err = vmShares(ws); err != nil || len(shares) != 0 {
		t.Fatalf("ephemeral profile shares = %+v, %v", shares, err)
	}
	if binds, err = vmGuestData(ws, cacheDir, nil, true); err != nil {
		t.Fatalf("vmGuestData: %v", err)
	}
	wantBinds = []guest.Bind{
		{Source: guest.ReadOnlyHostDir + "/config", Target: guestConfigDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache", Target: guestCacheDir, ReadOnly: true},
		{Source: guest.ReadOnlyHostDir + "/cache/tui-plugin", Target: tuiPluginContainerDir, ReadOnly: true},
		{Source: guest.HostDir + "/ephemeral/opencode-data", Target: dataDir},
		{Source: guest.HostDir + "/ephemeral/opencode-state", Target: stateDir},
		{Source: guest.HostDir + "/ephemeral/opencode1-omo", Target: oc.State[2].Path},
		{Source: guest.HostDir + "/home/cache", Target: guestUserCache},
	}
	if !slices.Equal(binds, wantBinds) {
		t.Fatalf("ephemeral binds =\n%+v\nwant\n%+v", binds, wantBinds)
	}
	leftover := filepath.Join(vm.GuestDataDir(vmStateDir("w")), "ephemeral", "opencode-data", "session.json")
	if err := os.WriteFile(leftover, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := vmGuestData(ws, cacheDir, nil, false); err != nil {
		t.Fatalf("vmGuestData reload: %v", err)
	}
	if _, err := os.Stat(leftover); err != nil {
		t.Fatalf("reload emptied ephemeral state: %v", err)
	}
	if _, err := vmGuestData(ws, cacheDir, nil, true); err != nil {
		t.Fatalf("vmGuestData start: %v", err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("start kept ephemeral state: %v", err)
	}
}

func TestClaimProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := config.NamedProfile("work")
	ws := func(name string) *workspace.Resolved {
		return &workspace.Resolved{Name: name, AgentProfile: &profile}
	}
	lock := filepath.Join(profilesDir(), "work", "lock")

	if err := claimProfile(ws("a")); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if b, _ := os.ReadFile(lock); string(b) != "a\n" { //nolint:gosec // G304: test file
		t.Fatalf("lock = %q, want a", b)
	}
	// The VM of a does not run: its claim is stale.
	if err := claimProfile(ws("b")); err != nil {
		t.Fatalf("take over stale claim: %v", err)
	}
	if err := releaseProfile("a", "work"); err != nil {
		t.Fatalf("release by non-owner: %v", err)
	}
	if b, _ := os.ReadFile(lock); string(b) != "b\n" { //nolint:gosec // G304: test file
		t.Fatalf("lock after foreign release = %q, want b", b)
	}
	if err := releaseProfile("b", "work"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if b, _ := os.ReadFile(lock); len(b) != 0 { //nolint:gosec // G304: test file
		t.Fatalf("lock after release = %q", b)
	}

	// A running VM of b with the profile holds it.
	state := vmStateDir("b")
	if err := vm.WriteSpec(vm.Spec{StateDir: state, Workspace: "b", Profile: "work"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vm.StateFile(state), fmt.Appendf(nil, `{"pid":%d,"ready":true}`, os.Getpid()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := claimProfile(ws("b")); err != nil {
		t.Fatalf("claim b: %v", err)
	}
	err := claimProfile(ws("a"))
	assertContains(t, fmt.Sprint(err), `profile "work" is in use by running workspace "b"`)
}

func TestCheckNestedShares(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	project := filepath.Join(dir, "project")
	outside := filepath.Join(dir, "outside")
	for _, d := range []string{filepath.Join(project, "real"), outside} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(project, "link")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	// A path that leaves through an alias and comes back through a link
	// inside the read-write share.
	external := filepath.Join(dir, "external")
	if err := os.Symlink(filepath.Join(alias, "link"), external); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		shares  []vm.Share
		wantErr string
	}{
		{"link reached through an alias", []vm.Share{{HostPath: project}, {HostPath: filepath.Join(alias, "link", "child"), GuestPath: "/data"}}, "symbolic link"},
		{"link reached through an outside link", []vm.Share{{HostPath: project}, {HostPath: filepath.Join(external, "child"), GuestPath: "/data"}}, "symbolic link"},
		{"alias of the read-write share itself", []vm.Share{{HostPath: project}, {HostPath: alias, GuestPath: "/data"}}, ""},
		{"nested directory", []vm.Share{{HostPath: project}, {HostPath: filepath.Join(project, "real"), GuestPath: "/data"}}, ""},
		{"separate directories", []vm.Share{{HostPath: project}, {HostPath: outside, GuestPath: "/data"}}, ""},
		{"link in read-only share", []vm.Share{{HostPath: project, ReadOnly: true}, {HostPath: filepath.Join(project, "link"), GuestPath: "/data"}}, ""},
		{"link in read-write share", []vm.Share{{HostPath: project}, {HostPath: filepath.Join(project, "link", "x"), GuestPath: "/data"}}, "symbolic link"},
		{"link below an alias of a read-write share", []vm.Share{{HostPath: alias}, {HostPath: filepath.Join(project, "link"), GuestPath: "/data"}}, "symbolic link"},
		{"directory below an alias of a read-write share", []vm.Share{{HostPath: alias}, {HostPath: filepath.Join(project, "real"), GuestPath: "/data"}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := checkNestedShares(tt.shares)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatalf("checkNestedShares() = nil, want %q", tt.wantErr)
			}
			assertContains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestDanglingShareLinks(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dotfiles := filepath.Join(home, "dotfiles")
	project := filepath.Join(home, "project")
	for _, d := range []string{dotfiles, filepath.Join(project, "sub")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	instructions := filepath.Join(dotfiles, "AGENTS.md")
	if err := os.WriteFile(instructions, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		filepath.Join(project, "AGENTS.md"): instructions,
		filepath.Join(project, "local"):     "sub",
		filepath.Join(project, "system"):    "/bin",
		filepath.Join(project, "broken"):    filepath.Join(home, "missing"),
		filepath.Join(dotfiles, "relative"): "AGENTS.md",
		filepath.Join(dotfiles, "absolute"): instructions,
	}
	for link, target := range links {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	projectShare := vm.Share{HostPath: project, GuestPath: project}
	dotfilesAtHostPath := vm.Share{HostPath: dotfiles, GuestPath: dotfiles, ReadOnly: true}
	dotfilesRemapped := vm.Share{HostPath: dotfiles, GuestPath: "/home/agent/.dotfiles", ReadOnly: true}
	tests := []struct {
		name      string
		shares    []vm.Share
		snapshots []string
		want      []string
	}{
		{"target not shared", []vm.Share{projectShare}, nil, []string{filepath.Join(project, "AGENTS.md")}},
		{"target shared at its host path", []vm.Share{projectShare, dotfilesAtHostPath}, nil, nil},
		{"target snapshotted at its host path", []vm.Share{projectShare}, []string{instructions}, nil},
		{"absolute link in a remapped mount", []vm.Share{dotfilesRemapped}, nil, []string{filepath.Join(dotfiles, "absolute")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			covered := slices.Clone(tt.snapshots)
			for _, s := range tt.shares {
				covered = append(covered, s.GuestPath)
			}
			var got []string
			for _, l := range danglingShareLinks(tt.shares, covered, home) {
				got = append(got, l.link)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("danglingShareLinks() links = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDanglingLinkWarningEscapesPaths(t *testing.T) {
	t.Parallel()
	got := danglingLinkWarning("ws", shareLink{
		link:   "/p/evil\x1b]0;title\x07",
		target: "/home/u/\x1b[2Jx",
		guest:  "/home/u/\x1b[2Jx",
	})
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Fatalf("danglingLinkWarning() = %q, contains raw control characters", got)
	}
	assertContains(t, got, `"/p/evil\x1b]0;title\a"`)
}

func TestReadRegular(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.json"), make([]byte, 11), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if data, err := readRegular(root, "ok.json", 10); err != nil || string(data) != "{}" {
		t.Fatalf("readRegular(ok.json) = %q, %v", data, err)
	}
	for name, want := range map[string]string{"big.json": "larger than", "fifo.json": "not a regular file", "link.json": "escapes"} {
		_, err := readRegular(root, name, 10)
		if err == nil {
			t.Fatalf("readRegular(%s) = nil error", name)
		}
		assertContains(t, err.Error(), want)
	}
}

func TestVMPolicyHTTPRules(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel: HOME decides where the
	// inspection CA is stored.
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := &workspace.Resolved{
		Name: "w",
		NetworkRules: []config.NetworkRule{
			{Name: "plain", Allow: []string{"example.com"}},
			{Name: "gh", Allow: []string{"api.github.com"}, HTTP: &config.HTTPMatch{Methods: []string{"get", "Head", "GET"}, Paths: []string{"/repos/seznam/*", "/repos/seznam/*"}}},
			{Name: "any-method", Allow: []string{"*.example.org"}, HTTP: &config.HTTPMatch{Methods: []string{"get", "*"}}},
			{Name: "no-delete", Deny: []string{"10.0.0.0/8"}, Proto: "tcp", Ports: []config.RulePort{"8080"}, HTTP: &config.HTTPMatch{Methods: []string{"delete"}}},
			{Name: "empty", Allow: []string{"empty.example"}, HTTP: &config.HTTPMatch{}},
		},
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	var got struct {
		Deny    []vmRule   `json:"deny"`
		Allow   []vmRule   `json:"allow"`
		Inspect *vmInspect `json:"inspect"`
	}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	if len(got.Allow) != 4 || len(got.Deny) != 1 {
		t.Fatalf("policy = %s", data)
	}
	if got.Allow[0].HTTP != nil {
		t.Fatalf("plain rule has http: %+v", got.Allow[0])
	}
	gh := got.Allow[1].HTTP
	if gh == nil || !slices.Equal(gh.Methods, []string{"GET", "HEAD"}) || !slices.Equal(gh.Paths, []string{"/repos/seznam/*"}) {
		t.Fatalf("gh http = %+v", gh)
	}
	if h := got.Allow[2].HTTP; h == nil || !slices.Equal(h.Methods, []string{"*"}) || len(h.Paths) != 0 {
		t.Fatalf("any-method http = %+v", h)
	}
	if h := got.Deny[0].HTTP; h == nil || !slices.Equal(h.Methods, []string{"DELETE"}) || got.Deny[0].Proto != "tcp" || !slices.Equal(got.Deny[0].CIDRs, []string{"10.0.0.0/8"}) {
		t.Fatalf("deny rule = %+v", got.Deny[0])
	}
	// An empty http table must survive as an object: it means inspect everything.
	assertContains(t, string(data), `"name":"empty","names":["empty.example"],"proto":"tcp","http":{}`)
	assertContains(t, string(data), `"name":"gh","names":["api.github.com"],"proto":"tcp","http":{"methods":["GET","HEAD"],"paths":["/repos/seznam/*"]}`)

	stateDir := filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "w")
	want := &vmInspect{CACert: filepath.Join(stateDir, "inspect-ca.pem"), CAKey: filepath.Join(stateDir, "inspect-ca.key")}
	if got.Inspect == nil || *got.Inspect != *want {
		t.Fatalf("inspect = %+v, want %+v", got.Inspect, want)
	}
	if _, err := os.Stat(got.Inspect.CACert); err != nil {
		t.Fatalf("CA certificate not created: %v", err)
	}
	if strings.Contains(string(data), "BEGIN") {
		t.Fatalf("policy embeds key material: %s", data)
	}
}

func TestVMBuilderPolicyHTTPRules(t *testing.T) {
	// t.Setenv is incompatible with t.Parallel: HOME decides where the
	// inspection CA would be stored. Stderr is swapped to read the warning.
	home := t.TempDir()
	t.Setenv("HOME", home)
	ws := &workspace.Resolved{
		Name: "w",
		NetworkRules: []config.NetworkRule{
			{Name: "plain", Allow: []string{"example.com"}},
			{Name: "gh", Allow: []string{"api.github.com"}, Ports: []config.RulePort{"443"}, HTTP: &config.HTTPMatch{Methods: []string{"get"}, Paths: []string{"/repos/*"}}},
			{Name: "no-delete", Deny: []string{"10.0.0.0/8"}, HTTP: &config.HTTPMatch{Methods: []string{"delete"}}},
			{Name: "no", Deny: []string{"bad.example"}},
		},
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	data, err := vmBuilderPolicy(context.Background(), ws)
	os.Stderr = stderr
	_ = w.Close()
	warning, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("vmBuilderPolicy: %v", err)
	}

	const want = `{"deny":[{"name":"no","names":["bad.example"],"proto":"tcp"}],"allow":[{"name":"plain","names":["example.com"],"proto":"tcp"},{"name":"gh","names":["api.github.com"],"proto":"tcp","ports":["443"]}]}`
	if string(data) != want {
		t.Fatalf("builder policy = %s\nwant %s", data, want)
	}
	for _, unwanted := range []string{`"http"`, `"inspect"`, "no-delete"} {
		if strings.Contains(string(data), unwanted) {
			t.Fatalf("builder policy contains %s: %s", unwanted, data)
		}
	}
	assertContains(t, string(warning), "WARNING: the isolated image build does not inspect HTTPS")
	if _, err := os.Stat(filepath.Join(home, ".cache", "jailoc-vm", "workspaces", "w", "inspect-ca.pem")); !os.IsNotExist(err) {
		t.Fatalf("builder policy created the inspection CA: %v", err)
	}

	// The workspace's own policy is unchanged.
	full, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	assertContains(t, string(full), `"http":{"methods":["GET"],"paths":["/repos/*"]}`)
	assertContains(t, string(full), `"inspect"`)
}

func TestVMBuilderPolicyWithoutHTTPRulesIsUnchanged(t *testing.T) {
	// No HTTP rules: same policy as the workspace's, and no warning.
	ws := &workspace.Resolved{
		Name:         "no-http-builder-test",
		NetworkRules: []config.NetworkRule{{Name: "dev", Allow: []string{"github.com"}}, {Name: "no", Deny: []string{"bad.example"}}},
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = w
	builder, err := vmBuilderPolicy(context.Background(), ws)
	os.Stderr = stderr
	_ = w.Close()
	warning, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("vmBuilderPolicy: %v", err)
	}
	full, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	if string(builder) != string(full) {
		t.Fatalf("builder policy = %s, want %s", builder, full)
	}
	if len(warning) != 0 {
		t.Fatalf("warning = %q, want none", warning)
	}
}

func TestVMPolicyHTTPUpstreamCA(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rules := []config.NetworkRule{{Name: "gh", Allow: []string{"api.github.com"}, HTTP: &config.HTTPMatch{}}}
	tests := []struct {
		name   string
		bundle config.CABundle
		want   string
	}{
		{"automatic", config.AutomaticCABundle(), ""},
		{"disabled", config.DisabledCABundle(), ""},
		{"custom absolute", config.CustomCABundle("/etc/corp/ca.pem"), "/etc/corp/ca.pem"},
		{"custom tilde", config.CustomCABundle("~/corp/ca.pem"), filepath.Join(home, "corp", "ca.pem")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, err := vmPolicy(context.Background(), &workspace.Resolved{Name: "w", NetworkRules: rules, CABundle: tc.bundle})
			if err != nil {
				t.Fatalf("vmPolicy: %v", err)
			}
			var got struct {
				Inspect *vmInspect `json:"inspect"`
			}
			if err := json.Unmarshal(data, &got); err != nil || got.Inspect == nil {
				t.Fatalf("policy = %s, err = %v", data, err)
			}
			if got.Inspect.UpstreamCA != tc.want {
				t.Fatalf("upstream_ca = %q, want %q", got.Inspect.UpstreamCA, tc.want)
			}
			if tc.want == "" && strings.Contains(string(data), "upstream_ca") {
				t.Fatalf("empty upstream_ca is serialized: %s", data)
			}
		})
	}
}

func TestVMPolicyWithoutHTTPRulesIsUnchanged(t *testing.T) {
	// No HOME override: without http rules no CA may be created at all.
	ws := &workspace.Resolved{
		Name:         "no-http-policy-test",
		NetworkRules: []config.NetworkRule{{Name: "dev", Allow: []string{"github.com"}}, {Name: "no", Deny: []string{"bad.example"}}},
		CABundle:     config.CustomCABundle("/etc/corp/ca.pem"),
	}
	data, err := vmPolicy(context.Background(), ws)
	if err != nil {
		t.Fatalf("vmPolicy: %v", err)
	}
	const want = `{"deny":[{"name":"no","names":["bad.example"],"proto":"tcp"}],"allow":[{"name":"dev","names":["github.com"],"proto":"tcp"}]}`
	if string(data) != want {
		t.Fatalf("policy = %s, want %s", data, want)
	}
}

func TestCompileVMRuleRejectsInvalidHTTP(t *testing.T) {
	t.Parallel()
	for _, h := range []*config.HTTPMatch{
		{Methods: []string{"GET POST"}},
		{Methods: []string{""}},
		{Paths: []string{"repos"}},
		{Paths: []string{"/a/**/b"}},
	} {
		if _, err := compileVMRule("r", "tcp", nil, []string{"example.com"}, h); err == nil {
			t.Errorf("compileVMRule(%+v) error = nil", h)
		}
	}
}
