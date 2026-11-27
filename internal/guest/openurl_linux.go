//go:build linux

package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/stacklok/go-microvm/net/topology"
)

const (
	openURLTimeout = 5 * time.Second
	// xdgOpenScript execs jailoc-guest so its exit status (0 once the host
	// opened the URL) reaches the caller unchanged.
	xdgOpenScript = "#!/bin/sh\nexec " + BinaryPath + " open-url \"$@\"\n"
)

// OpenURL handles "jailoc-guest open-url <url>", invoked by the xdg-open
// wrapper. When <url> is an OAuth authorization URL whose redirect_uri targets
// guest loopback and a local process is already listening on that port, it asks
// the host broker to bind a single-shot HTTP callback proxy for that port and
// exact path and to open <url> in the host browser. It returns nil only after
// the broker answers 202 Accepted, meaning the host accepted the URL for
// launch (not that the browser finished loading it); any other status is an
// error. A missing or non-OAuth URL is never opened, so it returns an error
// rather than reporting success. The error text never includes the URL, which
// carries OAuth state.
func OpenURL(args []string) error {
	rawURL := firstURLArg(args)
	if rawURL == "" {
		return errors.New("open-url: no URL argument")
	}
	port, path, ok := parseOAuthRedirect(rawURL)
	if !ok {
		return errors.New("open-url: not a loopback OAuth authorization URL; not opened")
	}
	if !hasLoopbackListener(port) {
		return fmt.Errorf("open-url: no local listener on port %d", port)
	}
	endpoint := "http://" + net.JoinHostPort(topology.GatewayIP, strconv.Itoa(OAuthCallbackPort)) + "/register"
	return registerOAuthCallback(endpoint, port, path, rawURL)
}

func firstURLArg(args []string) string {
	for _, a := range args {
		if a == "--" {
			continue
		}
		if a != "" {
			return a
		}
	}
	return ""
}

// hasLoopbackListener passively inspects /proc/net/tcp and /proc/net/tcp6 to
// confirm a guest process is currently listening on port on loopback or
// wildcard without touching its accept queue.
func hasLoopbackListener(port uint16) bool {
	for _, src := range []struct {
		path string
		ipv6 bool
	}{
		{"/proc/net/tcp", false},
		{"/proc/net/tcp6", true},
	} {
		f, err := os.Open(src.path) //nolint:gosec // G304: fixed procfs paths
		if err != nil {
			continue
		}
		found := parseProcNetTCP(f, src.ipv6, port)
		_ = f.Close()
		if found {
			return true
		}
	}
	return false
}

func registerOAuthCallback(endpoint string, port uint16, path string, rawURL string) error {
	payload, err := json.Marshal(struct {
		Port uint16 `json:"port"`
		Path string `json:"path"`
		URL  string `json:"url"`
	}{Port: port, Path: path, URL: rawURL})
	if err != nil {
		return fmt.Errorf("open-url: encode request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), openURLTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("open-url: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// The control endpoint is the host gateway; never route it through an
	// HTTP_PROXY from the session environment.
	client := &http.Client{
		Timeout: openURLTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("open-url: register callback: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusBadGateway {
		return fmt.Errorf("open-url: host could not open browser (status %d); use jailoc auth on host", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusAccepted {
		// Only 202 means the host accepted the URL for launch. Any other status,
		// including a bare 200, never confirms an open. The response body and
		// status text are not echoed: they may reflect the authorization URL,
		// which carries OAuth state.
		return fmt.Errorf("open-url: broker did not accept the URL for opening: status %d", resp.StatusCode)
	}
	return nil
}

// installXDGOpen writes the xdg-open wrapper script under root/usr/local/bin.
func installXDGOpen(root string) error {
	target := filepath.Join(root, XDGOpenPath)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: /usr/local/bin must be traversable by agent
		return fmt.Errorf("create %s: %w", filepath.Dir(target), err)
	}
	if err := os.WriteFile(target, []byte(xdgOpenScript), 0o755); err != nil { //nolint:gosec // G306: xdg-open wrapper must be executable by agent
		return fmt.Errorf("write %s: %w", target, err)
	}
	return nil
}
