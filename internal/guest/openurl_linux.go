//go:build linux

package guest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	xdgOpenScript  = "#!/bin/sh\n" + BinaryPath + " open-url \"$@\"\nexit 1\n"
)

// OpenURL handles "jailoc-guest open-url <url>", invoked by the xdg-open
// wrapper. When <url> is an OAuth authorization URL whose redirect_uri targets
// guest loopback and a local process is already listening on that port, it asks
// the host broker to open a single-shot HTTP callback proxy for that port and
// exact path. Non-OAuth URLs are ignored silently.
func OpenURL(args []string) error {
	rawURL := firstURLArg(args)
	if rawURL == "" {
		return nil
	}
	port, path, ok := parseOAuthRedirect(rawURL)
	if !ok {
		return nil
	}
	if !hasLoopbackListener(port) {
		return fmt.Errorf("open-url: no local listener on port %d", port)
	}
	endpoint := "http://" + net.JoinHostPort(topology.GatewayIP, strconv.Itoa(OAuthCallbackPort)) + "/register"
	return registerOAuthCallback(endpoint, port, path)
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

func registerOAuthCallback(endpoint string, port uint16, path string) error {
	payload, err := json.Marshal(struct {
		Port uint16 `json:"port"`
		Path string `json:"path"`
	}{Port: port, Path: path})
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
	client := &http.Client{Timeout: openURLTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("open-url: register callback: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("open-url: broker returned %s: %s", resp.Status, bytes.TrimSpace(msg))
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
