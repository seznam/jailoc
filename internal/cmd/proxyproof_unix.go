//go:build darwin || linux

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/seznam/jailoc/internal/proxyhost"
	"github.com/spf13/cobra"
)

const proxyProofHeader = "Isolated host proof (not enrollment)"

// newProxyHostProofCmd builds `jailoc proxy host-proof`, an opt-in,
// unprivileged check of the host primitives a future proxy enrollment
// relies on. It runs entirely inside a temporary fixture.
func newProxyHostProofCmd() *cobra.Command {
	return newProxyHostProofCmdWith(proxyhost.RunProof, os.Geteuid)
}

// newProxyHostProofCmdWith builds the command around the proof runner and
// the effective-uid lookup, so tests can exercise reporting and the root refusal.
func newProxyHostProofCmdWith(run func(context.Context) (proxyhost.ProofResult, error), euid func() int) *cobra.Command {
	return &cobra.Command{
		Use:   "host-proof",
		Short: "Run an isolated, unprivileged host proof (not enrollment)",
		Long: "Run a bounded, unprivileged proof of the host primitives a proxy enrollment would rely on. This is not enrollment.\n\n" +
			"The proof works only on a temporary fixture and in memory: a loopback listener broker, per-connection forwarding, TLS with a\n" +
			"generated in-memory certificate authority, and a hosts-file plan on a temporary copy. It reads no VM, configuration or\n" +
			"certificate file.\n\n" +
			"It never changes DNS, trust stores, services, the firewall, the system hosts file or system port 443, and it refuses\n" +
			"to run as root. A pass covers only the rows it prints; it is not a claim that setup is complete.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runProxyHostProof(cmd, run, euid)
		},
	}
}

func runProxyHostProof(cmd *cobra.Command, run func(context.Context) (proxyhost.ProofResult, error), euid func() int) error {
	if euid() == 0 {
		return errors.New("host-proof refuses to run as root (effective uid 0); run it as your own user")
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("host proof not started: %w", err)
	}
	res, runErr := run(ctx)
	out := cmd.OutOrStdout()
	failed, err := printProxyProof(out, res)
	if err != nil {
		return fmt.Errorf("report host proof: %w", err)
	}
	switch {
	case runErr != nil:
		return fmt.Errorf("host proof: %w", runErr)
	case len(res.Checks) == 0:
		return errors.New("host proof ran no checks")
	case failed > 0:
		return fmt.Errorf("host proof: %d check(s) failed", failed)
	}
	return writeProxyProofLines(out,
		"",
		"All checked rows passed. This is not full setup success: no proxy, DNS name, trust or enrollment was set up.",
		"host-proof never changes DNS, trust stores, services, the firewall, the system hosts file or system port 443.",
	)
}

// printProxyProof writes the header and one row per check and returns how
// many checks failed.
func printProxyProof(w io.Writer, res proxyhost.ProofResult) (failed int, err error) {
	if err := writeProxyProofLines(w, proxyProofHeader, ""); err != nil {
		return 0, fmt.Errorf("write host proof header: %w", err)
	}
	for _, c := range res.Checks {
		status := "PASS"
		if !c.Passed {
			status = "FAIL"
			failed++
		}
		if _, err := fmt.Fprintf(w, "%s  %s: %s\n", status, c.Name, c.Detail); err != nil {
			return failed, fmt.Errorf("write host proof output: %w", err)
		}
	}
	return failed, nil
}

func writeProxyProofLines(w io.Writer, lines ...string) error {
	for _, l := range lines {
		if _, err := fmt.Fprintln(w, l); err != nil {
			return fmt.Errorf("write host proof output: %w", err)
		}
	}
	return nil
}
