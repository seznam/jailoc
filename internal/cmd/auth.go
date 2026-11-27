package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/spf13/cobra"
)

// authMaxLinks is the most pending authorization links the broker keeps.
const authMaxLinks = 8

var (
	authPort int
	authOpen bool
)

var authCmd = &cobra.Command{
	Use:   "auth [workspace]",
	Short: "Print or open a pending sign-in link captured from a workspace VM",
	Long: `Hand a sign-in link that an agent in the workspace VM tried to open to a
browser on this host. The network broker keeps the link for a few minutes and
forwards the sign-in callback back into the VM.

By default the link is printed to standard output and nothing is launched;
guidance goes to standard error. With --open it is opened in the default
browser of this host instead. When several sign-ins are pending, they are
listed without their links and --port selects one.

The link is a credential-bearing URL. jailoc does not persist authorization
links; the browser and your terminal history may still record it.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		port, err := authPortOption(cmd.Flags().Changed("port"), authPort)
		if err != nil {
			return err
		}
		ws, err := resolveInspectWorkspace(args)
		if err != nil {
			return err
		}
		return authVM(cmd, ws, authOptions{port: port, open: authOpen})
	},
}

func init() {
	authCmd.Flags().IntVar(&authPort, "port", 0, "select the pending sign-in with this callback port")
	authCmd.Flags().BoolVar(&authOpen, "open", false, "open the link in the default browser of this host instead of printing it")
	rootCmd.AddCommand(authCmd)
}

// authOptions select and handle one pending link. port 0 means any.
type authOptions struct {
	port int
	open bool
}

// authPortOption validates an explicitly given --port.
func authPortOption(changed bool, port int) (int, error) {
	if !changed {
		return 0, nil
	}
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("--port %d is out of range 1-65535", port)
	}
	return port, nil
}

// runAuthLinks prints or opens the pending link selected by opts. Links are
// validated before anything is shown and expiry is judged by now. The URL
// reaches out only (default) or open (--open); errors and errOut never carry it.
func runAuthLinks(
	ctx context.Context,
	out, errOut io.Writer,
	links []netbroker.OAuthLink,
	opts authOptions,
	now func() time.Time,
	open func(ctx context.Context, rawURL string) error,
) error {
	if err := validateAuthLinks(links); err != nil {
		return err
	}
	sorted := slices.Clone(links)
	slices.SortFunc(sorted, func(a, b netbroker.OAuthLink) int { return int(a.Port) - int(b.Port) })

	live := make([]netbroker.OAuthLink, 0, len(sorted))
	for _, l := range sorted {
		if authLinkLive(l, now()) {
			live = append(live, l)
		}
	}

	link, err := selectAuthLink(errOut, sorted, live, opts.port, now())
	if err != nil {
		return err
	}
	// Selection took time; the link must still be unexpired.
	remaining := link.Expires.Sub(now())
	if remaining <= 0 {
		return authExpiredError(link.Port)
	}

	if !opts.open {
		if _, err := fmt.Fprintln(out, link.URL); err != nil {
			return fmt.Errorf("write authorization link: %w", err)
		}
		_, _ = fmt.Fprintf(errOut, "Open the link above in a browser on this machine to continue sign-in (callback port %d, expires in %s).\n",
			link.Port, formatAuthRemaining(remaining))
		return nil
	}

	_, _ = fmt.Fprintf(errOut, "Opening sign-in page at %s (callback port %d, expires in %s).\n",
		authOrigin(link.URL), link.Port, formatAuthRemaining(remaining))
	if err := open(ctx, link.URL); err != nil {
		return authOpenFailure(ctx, err)
	}
	return nil
}

// authOpenFailure turns any opener error into one that cannot carry the URL:
// the opener's error is dropped, only a canceled context is kept.
func authOpenFailure(ctx context.Context, _ error) error {
	const msg = "open sign-in page (run without --open to print the link): "
	if ctx.Err() != nil {
		return fmt.Errorf("%scanceled: %w", msg, ctx.Err())
	}
	return errors.New(msg + "the browser opener failed")
}

// validateAuthLinks rejects a response that is too large, has an invalid
// link or repeats a port. Errors never carry link contents.
func validateAuthLinks(links []netbroker.OAuthLink) error {
	if len(links) > authMaxLinks {
		return fmt.Errorf("broker returned %d authorization links, at most %d are allowed", len(links), authMaxLinks)
	}
	seen := make(map[uint16]bool, len(links))
	for i, l := range links {
		if err := netbroker.ValidateOAuthLink(l); err != nil {
			return fmt.Errorf("authorization link %d from the broker is invalid", i+1)
		}
		if seen[l.Port] {
			return fmt.Errorf("broker returned several authorization links for port %d", l.Port)
		}
		seen[l.Port] = true
	}
	return nil
}

func authLinkLive(l netbroker.OAuthLink, now time.Time) bool { return l.Expires.After(now) }

// selectAuthLink picks the link to hand over: the only live one, or the one
// for the requested port. all and live are sorted by port.
func selectAuthLink(errOut io.Writer, all, live []netbroker.OAuthLink, port int, now time.Time) (netbroker.OAuthLink, error) {
	if port != 0 {
		for _, l := range all {
			if int(l.Port) != port {
				continue
			}
			if !authLinkLive(l, now) {
				return netbroker.OAuthLink{}, authExpiredError(l.Port)
			}
			return l, nil
		}
		return netbroker.OAuthLink{}, fmt.Errorf("no pending sign-in for callback port %d%s", port, authPortList(live))
	}
	switch len(live) {
	case 0:
		return netbroker.OAuthLink{}, errors.New("no pending sign-in; start the sign-in in the workspace first (links expire after a few minutes)")
	case 1:
		return live[0], nil
	}
	_, _ = fmt.Fprintln(errOut, "Several sign-ins are pending:")
	for _, l := range live {
		_, _ = fmt.Fprintf(errOut, "  port %d  %s  expires in %s\n", l.Port, authOrigin(l.URL), formatAuthRemaining(l.Expires.Sub(now)))
	}
	return netbroker.OAuthLink{}, errors.New("several sign-ins are pending; select one with --port")
}

func authExpiredError(port uint16) error {
	return fmt.Errorf("the sign-in for callback port %d expired; start it again in the workspace", port)
}

func authPortList(live []netbroker.OAuthLink) string {
	if len(live) == 0 {
		return ""
	}
	ports := make([]string, len(live))
	for i, l := range live {
		ports[i] = strconv.Itoa(int(l.Port))
	}
	return " (pending: " + strings.Join(ports, ", ") + ")"
}

// authOrigin returns scheme://host of a URL that validateAuthLinks accepted,
// without credentials, path or query.
func authOrigin(raw string) string {
	u, _ := url.Parse(raw) // cannot fail: the link was validated and ValidateOAuthLink parses it
	return u.Scheme + "://" + u.Host
}

func formatAuthRemaining(d time.Duration) string {
	return d.Round(time.Second).String()
}
