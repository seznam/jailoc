//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/seznam/jailoc/internal/browser"
	"github.com/seznam/jailoc/internal/netbroker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
	"github.com/spf13/cobra"
)

const (
	authRequestTimeout = 5 * time.Second
	// authMaxBody bounds a response: authMaxLinks entries, each a URL of at
	// most 8 KiB and a path of at most 2 KiB whose every byte json.Marshal
	// may escape to a 6-byte \u00XX (& < >), plus per-entry JSON overhead.
	authMaxBody = authMaxLinks * (6*8192 + 6*2048 + 512)
)

// authVM hands a pending sign-in link of a running workspace VM to the user.
// It only talks to the broker's OAuth socket: no VM boot, SSH or secrets.
func authVM(cmd *cobra.Command, ws *workspace.Resolved, opts authOptions) error {
	stateDir := vmStateDir(ws.Name)
	st, err := runningVM(stateDir)
	if err != nil {
		return fmt.Errorf("read vm state of workspace %q: %w", ws.Name, err)
	}
	if st == nil {
		return fmt.Errorf("workspace %q is not running; start it with `jailoc up %s`", ws.Name, ws.Name)
	}
	links, err := fetchOAuthLinks(cmd.Context(), vm.OAuthSocket(stateDir), authRequestTimeout)
	if err != nil {
		if errors.Is(err, errAuthNoSocket) {
			return fmt.Errorf("the running VM of workspace %q has no sign-in socket; run `jailoc restart %s` to start a broker that supports it", ws.Name, ws.Name)
		}
		return fmt.Errorf("workspace %q: %w", ws.Name, err)
	}
	return runAuthLinks(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), links, opts, time.Now, hostOpenURL)
}

var errAuthNoSocket = errors.New("broker sign-in socket is missing")

// fetchOAuthLinks reads the pending links from the broker's private Unix
// socket. The client ignores proxy settings, follows no redirect, bounds the
// response and decodes it strictly. Errors never carry response contents.
func fetchOAuthLinks(ctx context.Context, socket string, timeout time.Duration) ([]netbroker.OAuthLink, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := newAuthClient(socket, timeout)
	defer client.CloseIdleConnections()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://jailoc-oauth/pending", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, errAuthNoSocket
		}
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
			return nil, errors.New("the broker did not answer in time")
		}
		return nil, errors.New("the broker sign-in socket is not reachable")
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errAuthNoSocket
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("the broker answered with status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, authMaxBody+1))
	if err != nil {
		return nil, errors.New("read the broker response failed")
	}
	if len(body) > authMaxBody {
		return nil, errors.New("the broker response is too large")
	}
	return decodeOAuthLinks(body)
}

// newAuthClient returns an HTTP client that only reaches the Unix socket: no
// proxy, no redirects, no connection reuse.
func newAuthClient(socket string, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// decodeOAuthLinks decodes a JSON array of links rejecting unknown fields and
// anything after the array.
func decodeOAuthLinks(body []byte) ([]netbroker.OAuthLink, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var links []netbroker.OAuthLink
	if err := dec.Decode(&links); err != nil || links == nil {
		return nil, errors.New("the broker response is malformed")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("the broker response is malformed")
	}
	return links, nil
}

// hostLauncher is the one process-wide opener launcher, so its bound on live
// openers holds across hostOpenURL calls.
var hostLauncher = browser.NewLauncher()

// hostOpenURL opens rawURL with the opener of this host OS. It waits about two
// seconds for the opener and never kills it: a foreground opener may run for
// the whole browser lifetime. Its errors never carry the URL.
func hostOpenURL(ctx context.Context, rawURL string) error {
	return hostLauncher.OpenURL(ctx, rawURL)
}
