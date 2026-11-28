package cmd

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

// netbrokerCmd runs the network broker of a vm-backend workspace. The vm
// supervisor starts it by re-executing the jailoc binary, so the broker ships
// inside jailoc instead of as a separate binary.
var netbrokerCmd = &cobra.Command{
	Use:                "netbroker",
	Short:              "Run the network broker of a vm-backend workspace (internal)",
	Hidden:             true,
	DisableFlagParsing: true,
	SilenceUsage:       true,
	RunE: func(_ *cobra.Command, args []string) error {
		return RunNetbroker(args)
	},
}

func init() {
	rootCmd.AddCommand(netbrokerCmd)
}

// RunNetbroker runs the network broker command line (args without the
// program name) with jailoc's secret resolver for the upstream proxy
// password, so the broker shares the secret sources and the TTL cache of
// [<scope>.secrets]. The hidden "jailoc netbroker" command and the
// standalone jailoc-netbroker binary both run it.
func RunNetbroker(args []string) error {
	return netbroker.MainWithOptions(args, netbroker.MainOptions{ResolveUpstreamAuth: resolveUpstreamAuth})
}

// upstreamAuthSecretName names the upstream proxy password in the secret
// cache and in errors. It is reserved: secret names are environment
// variable or file names, which never contain a dot, and the password is
// never delivered to the guest.
const upstreamAuthSecretName = "upstream_proxy.auth"

// upstreamAuthTimeout bounds one password read, below the broker's start
// timeout; a password manager that needs the terminal cannot prompt in the
// detached broker anyway.
var upstreamAuthTimeout = 10 * time.Second

// upstreamAuthSecret is the secret the password of auth is read as. A file
// secret keeps the value as it is; the broker trims a trailing line break
// and rejects other line breaks itself.
func upstreamAuthSecret(auth netbroker.UpstreamAuth) (workspace.ResolvedSecret, error) {
	s := workspace.ResolvedSecret{
		Name:        upstreamAuthSecretName,
		Kind:        workspace.SecretKindFile,
		FromEnv:     auth.FromEnv,
		FromFile:    auth.FromFile,
		FromCommand: auth.FromCommand,
	}
	if auth.TTL != "" {
		d, err := time.ParseDuration(auth.TTL)
		if err != nil || d <= 0 {
			return workspace.ResolvedSecret{}, fmt.Errorf("%s: invalid ttl %q: must be a positive duration", upstreamAuthSecretName, auth.TTL)
		}
		s.TTL = d
	}
	return s, nil
}

// resolveUpstreamAuth reads the upstream proxy password of wsName for the
// broker. It runs in the detached broker process: from_env reads the
// environment the broker inherited when the VM started, so a changed host
// variable takes effect only after `jailoc restart`; from_file and
// from_command are read anew whenever the broker needs the password, and a
// from_command with ttl shares the keychain cache with the workspace's
// secrets. The command gets no terminal and its stderr is dropped, and the
// value stays in the broker's memory. Errors name the source, never the
// value.
func resolveUpstreamAuth(ctx context.Context, wsName string, auth netbroker.UpstreamAuth) ([]byte, error) {
	s, err := upstreamAuthSecret(auth)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(detachedSecretCommands(ctx), upstreamAuthTimeout)
	defer cancel()
	return readSecret(ctx, wsName, s, false)
}

// upstreamPolicyProxy describes p for the broker policy of the workspace
// named name: the proxy URL or command, the CA and direct targets, and the
// password source. It never reads a credential. A missing or disabled proxy
// is nil.
func upstreamPolicyProxy(name string, p *config.UpstreamProxy) *netbroker.UpstreamProxy {
	if !p.IsEnabled() {
		return nil
	}
	out := &netbroker.UpstreamProxy{Workspace: name, URL: p.URL, Command: p.Command, CA: p.CA, Direct: slices.Clone(p.Direct)}
	if p.Auth != nil {
		out.Auth = &netbroker.UpstreamAuth{
			Username:    p.Auth.Username,
			FromEnv:     p.Auth.FromEnv,
			FromFile:    p.Auth.FromFile,
			FromCommand: p.Auth.FromCommand,
			TTL:         p.Auth.TTL,
		}
	}
	return out
}

// builderWorkspace is the workspace name the image builder VMs present to
// the upstream proxy and the secret cache.
const builderWorkspace = "builder"

// builderUpstreamPolicyProxy is the upstream proxy of the image builder
// VMs: the static [defaults.network.upstream_proxy] only, never a
// workspace's own table or a command, under the name builderWorkspace.
func builderUpstreamPolicyProxy(cfg *config.Config) *netbroker.UpstreamProxy {
	return upstreamPolicyProxy(builderWorkspace, workspace.BuilderUpstreamProxy(cfg))
}
