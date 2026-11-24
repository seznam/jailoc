//go:build linux

package guest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestInstallXDGOpenAndRegister(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := installXDGOpen(root); err != nil {
		t.Fatalf("installXDGOpen: %v", err)
	}
	target := filepath.Join(root, XDGOpenPath)
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat %s: %v", target, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("mode = %v, want executable", info.Mode())
	}
	content, err := os.ReadFile(target) //nolint:gosec // G304: test path inside t.TempDir()
	if err != nil {
		t.Fatalf("read %s: %v", target, err)
	}
	if !strings.Contains(string(content), BinaryPath+" open-url") {
		t.Fatalf("script = %q, want open-url invocation", string(content))
	}

	var gotPort uint16
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Port uint16 `json:"port"`
			Path string `json:"path"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		gotPort, gotPath = req.Port, req.Path
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	if err := registerOAuthCallback(ts.URL+"/register", 19876, "/mcp/oauth/callback"); err != nil {
		t.Fatalf("registerOAuthCallback: %v", err)
	}
	if gotPort != 19876 || gotPath != "/mcp/oauth/callback" {
		t.Fatalf("registered (%d, %q), want (19876, /mcp/oauth/callback)", gotPort, gotPath)
	}
}

// TestRegisterOAuthCallbackIgnoresHTTPProxy must not call t.Parallel: it uses
// t.Setenv. 0.0.0.0 is not loopback, so http.ProxyFromEnvironment would route
// it through HTTP_PROXY, while a direct dial on Linux reaches the loopback
// test server.
func TestRegisterOAuthCallbackIgnoresHTTPProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyHits.Add(1)
		http.Error(w, "proxied", http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)

	var hits atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(ts.Close)

	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")

	endpoint := strings.Replace(ts.URL, "127.0.0.1", "0.0.0.0", 1) + "/register"
	if err := registerOAuthCallback(endpoint, 19876, "/cb"); err != nil {
		t.Fatalf("registerOAuthCallback with HTTP_PROXY set: %v (proxy hits: %d)", err, proxyHits.Load())
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("proxy hits = %d, want 0", proxyHits.Load())
	}
	if hits.Load() != 1 {
		t.Fatalf("broker hits = %d, want 1", hits.Load())
	}
}
