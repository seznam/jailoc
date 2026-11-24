//go:build linux

package guest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
