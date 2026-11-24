//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/spf13/cobra"
)

// runProxyProofCmd executes `proxy <args>` with the host-proof run and
// euid collaborators replaced, and reports the output and error.
func runProxyProofCmd(t *testing.T, ctx context.Context, run func(context.Context) (proxyhost.ProofResult, error), euid int, args ...string) (string, error) {
	gw := newProxyCmd()
	gw.RemoveCommand(mustFindChild(t, gw, "host-proof"))
	gw.AddCommand(newProxyHostProofCmdWith(run, func() int { return euid }))
	var out bytes.Buffer
	gw.SetOut(&out)
	gw.SetErr(&out)
	gw.SetArgs(args)
	err := gw.ExecuteContext(ctx)
	return out.String(), err
}

func passingProof() proxyhost.ProofResult {
	return proxyhost.ProofResult{Checks: []proxyhost.Check{
		{Name: "listen", Passed: true, Detail: "bound 127.0.0.1:49152"},
		{Name: "https", Passed: true, Detail: "login page served"},
		{Name: "cleanup", Passed: true, Detail: "temporary files removed"},
	}}
}

func TestProxyHostProofIsRegisteredUnderProxy(t *testing.T) {
	t.Parallel()
	gw := newProxyCmd()
	found, rest, err := gw.Find([]string{"host-proof"})
	if err != nil {
		t.Fatal(err)
	}
	if found.Name() != "host-proof" || found.Parent() != gw || len(rest) != 0 {
		t.Fatalf("Find(host-proof) = %q args %v, want the host-proof child", found.Name(), rest)
	}
	if found.HasSubCommands() {
		t.Error("host-proof must be a leaf command")
	}
}

func TestProxyHostProofKeepsForegroundCommandContract(t *testing.T) {
	t.Parallel()
	gw := newProxyCmd()
	if gw.Use != "proxy [workspace...]" {
		t.Errorf("Use = %q, want the foreground usage", gw.Use)
	}
	for _, name := range []string{"tls-cert", "tls-key"} {
		f := gw.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("proxy lacks --%s", name)
		}
		if f.Annotations[cobra.BashCompOneRequiredFlag] == nil {
			t.Errorf("--%s is no longer required on the foreground command", name)
		}
		if gw.PersistentFlags().Lookup(name) != nil {
			t.Errorf("--%s must not be persistent: host-proof would inherit it", name)
		}
	}
	// The workspace named host-proof stays reachable behind `--`.
	found, rest, err := gw.Find([]string{"--tls-cert", "missing", "--tls-key", "missing", "--", "host-proof"})
	if err != nil {
		t.Fatal(err)
	}
	if found != gw || len(rest) == 0 || rest[len(rest)-1] != "host-proof" {
		t.Fatalf("Find after -- = %q %v, want proxy with host-proof as a workspace argument", found.Name(), rest)
	}
}

func TestProxyHostProofAfterDoubleDashIsAWorkspace(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var calls atomic.Int32
	run := func(context.Context) (proxyhost.ProofResult, error) {
		calls.Add(1)
		return passingProof(), nil
	}
	_, err := runProxyProofCmd(t, t.Context(), run, 1000,
		"--tls-cert", dir+"/missing.pem", "--tls-key", dir+"/missing.key", "--", "host-proof")
	if err == nil || !strings.Contains(err.Error(), "load TLS certificate") {
		t.Fatalf("error = %v, want the foreground proxy to try the certificate", err)
	}
	if calls.Load() != 0 {
		t.Fatal("host-proof ran for a workspace argument after --")
	}
}

func TestProxyHostProofRequiresNoTLSFlags(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	run := func(context.Context) (proxyhost.ProofResult, error) {
		calls.Add(1)
		return passingProof(), nil
	}
	if _, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof"); err != nil {
		t.Fatalf("host-proof without TLS flags: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("run called %d times, want 1", calls.Load())
	}
}

func TestProxyHostProofRejectsExtraInput(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		args []string
	}{
		{"positional", []string{"host-proof", "extra"}},
		{"listen", []string{"host-proof", "--listen", "127.0.0.1:0"}},
		{"tls-cert", []string{"host-proof", "--tls-cert", "c.pem"}},
		{"tls-key", []string{"host-proof", "--tls-key", "k.pem"}},
		{"parent tls flags before child", []string{"--tls-cert", "c.pem", "--tls-key", "k.pem", "host-proof"}},
		{"paths", []string{"host-proof", "--hosts-file", "/etc/hosts"}},
		{"port", []string{"host-proof", "--port", "443"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			run := func(context.Context) (proxyhost.ProofResult, error) {
				calls.Add(1)
				return passingProof(), nil
			}
			if _, err := runProxyProofCmd(t, t.Context(), run, 1000, tt.args...); err == nil {
				t.Fatalf("%v accepted", tt.args)
			}
			if calls.Load() != 0 {
				t.Fatalf("%v ran the proof", tt.args)
			}
		})
	}
}

func TestProxyHostProofHelp(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	run := func(context.Context) (proxyhost.ProofResult, error) {
		calls.Add(1)
		return passingProof(), nil
	}
	out, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof", "--help")
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("--help ran the proof")
	}
	for _, want := range []string{"host-proof", "not enrollment", "DNS", "trust", "services", "firewall", "443"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"--listen", "--tls-cert", "--tls-key", "--config", "--workspace"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("help shows %q, host-proof takes no flags:\n%s", unwanted, out)
		}
	}
}

func TestProxyForegroundHelpAndMissingFlagsUnchanged(t *testing.T) {
	t.Parallel()
	gw := newProxyCmd()
	var out bytes.Buffer
	gw.SetOut(&out)
	gw.SetErr(&out)
	gw.SetArgs([]string{"--help"})
	if err := gw.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"proxy [workspace...]", "--listen", "--tls-cert", "--tls-key", "host-proof"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("proxy help lacks %q:\n%s", want, out.String())
		}
	}
	gw = newProxyCmd()
	gw.SetOut(&out)
	gw.SetErr(&out)
	gw.SetArgs(nil)
	err := gw.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), `"tls-cert"`) || !strings.Contains(err.Error(), `"tls-key"`) {
		t.Fatalf("bare proxy error = %v, want the required TLS flags named", err)
	}
}

func TestProxyHostProofOutput(t *testing.T) {
	t.Parallel()
	run := func(context.Context) (proxyhost.ProofResult, error) { return passingProof(), nil }
	out, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Isolated host proof (not enrollment)",
		"PASS", "listen", "bound 127.0.0.1:49152",
		"https", "cleanup", "temporary files removed",
		"not full setup",
		"DNS", "trust", "services", "firewall", "443",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "FAIL") {
		t.Errorf("passing proof printed FAIL:\n%s", out)
	}
	if !strings.HasPrefix(out, "Isolated host proof (not enrollment)") {
		t.Errorf("output must start with the header:\n%s", out)
	}
}

func TestProxyHostProofFailingCheckIsAnError(t *testing.T) {
	t.Parallel()
	run := func(context.Context) (proxyhost.ProofResult, error) {
		res := passingProof()
		res.Checks[1] = proxyhost.Check{Name: "https", Passed: false, Detail: "handshake refused"}
		return res, nil
	}
	out, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof")
	if err == nil {
		t.Fatalf("failing check returned nil error:\n%s", out)
	}
	for _, want := range []string{"PASS", "FAIL", "handshake refused", "cleanup"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not full setup") && strings.Contains(out, "All checked") {
		t.Errorf("failing proof claimed success:\n%s", out)
	}
}

func TestProxyHostProofEmptyResultIsAnError(t *testing.T) {
	t.Parallel()
	run := func(context.Context) (proxyhost.ProofResult, error) { return proxyhost.ProofResult{}, nil }
	if _, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof"); err == nil {
		t.Fatal("a proof that checked nothing succeeded")
	}
}

func TestProxyHostProofRunErrorKeepsRowsAndFails(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("fixture boom")
	run := func(context.Context) (proxyhost.ProofResult, error) {
		return proxyhost.ProofResult{Checks: []proxyhost.Check{
			{Name: "cleanup", Passed: true, Detail: "removed"},
		}}, sentinel
	}
	out, err := runProxyProofCmd(t, t.Context(), run, 1000, "host-proof")
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want it to wrap the run error", err)
	}
	if !strings.Contains(out, "cleanup") {
		t.Errorf("rows reported before the error are lost:\n%s", out)
	}
	if strings.Contains(out, "not full setup") {
		t.Errorf("errored proof printed the success footer:\n%s", out)
	}
}

func TestProxyHostProofCanceledContextHasNoSideEffects(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var calls atomic.Int32
	run := func(context.Context) (proxyhost.ProofResult, error) {
		calls.Add(1)
		return passingProof(), nil
	}
	out, err := runProxyProofCmd(t, ctx, run, 1000, "host-proof")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if calls.Load() != 0 {
		t.Fatal("proof ran with a canceled context")
	}
	if strings.Contains(out, "PASS") {
		t.Errorf("canceled run printed results:\n%s", out)
	}
}

func TestProxyHostProofPassesCommandContext(t *testing.T) {
	t.Parallel()
	type key struct{}
	ctx := context.WithValue(t.Context(), key{}, "marker")
	var got atomic.Value
	run := func(c context.Context) (proxyhost.ProofResult, error) {
		got.Store(c.Value(key{}))
		return passingProof(), nil
	}
	if _, err := runProxyProofCmd(t, ctx, run, 1000, "host-proof"); err != nil {
		t.Fatal(err)
	}
	if got.Load() != "marker" {
		t.Fatalf("run context = %v, want the command context", got.Load())
	}
}

func TestProxyHostProofRefusesRoot(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	run := func(context.Context) (proxyhost.ProofResult, error) {
		calls.Add(1)
		return passingProof(), nil
	}
	out, err := runProxyProofCmd(t, t.Context(), run, 0, "host-proof")
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("error = %v, want a message naming root", err)
	}
	if calls.Load() != 0 {
		t.Fatal("proof ran as root")
	}
	if strings.Contains(out, "PASS") {
		t.Errorf("root refusal printed results:\n%s", out)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestProxyHostProofReturnsWriteErrors(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("closed pipe")
	gw := newProxyCmd()
	gw.RemoveCommand(mustFindChild(t, gw, "host-proof"))
	gw.AddCommand(newProxyHostProofCmdWith(
		func(context.Context) (proxyhost.ProofResult, error) { return passingProof(), nil },
		func() int { return 1000 },
	))
	gw.SetOut(failingWriter{err: sentinel})
	gw.SetErr(&bytes.Buffer{})
	gw.SetArgs([]string{"host-proof"})
	if err := gw.ExecuteContext(t.Context()); !errors.Is(err, sentinel) {
		t.Fatalf("error = %v, want the write error", err)
	}
}

func mustFindChild(t *testing.T, parent *cobra.Command, name string) *cobra.Command {
	t.Helper()
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no child %q", name)
	return nil
}

// TestGatewayHostProofActualCommand runs the real library with no VM, config
// or certificate files: HOME and the config path point at an empty directory
// that must stay empty. It cannot be parallel because it sets the environment.
func TestGatewayHostProofActualCommand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("host-proof refuses to run as root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("XDG_CACHE_HOME", home)

	gw := newProxyCmd()
	var out bytes.Buffer
	gw.SetOut(&out)
	gw.SetErr(&out)
	gw.SetArgs([]string{"host-proof"})
	err := gw.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("actual host-proof: %v\n%s", err, out.String())
	}
	for _, want := range []string{"Isolated host proof (not enrollment)", "PASS"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "FAIL") {
		t.Errorf("actual proof reported FAIL:\n%s", out.String())
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("host-proof left files in HOME: %v", entries)
	}
}
