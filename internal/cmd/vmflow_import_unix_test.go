//go:build darwin || linux

package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/vm"
	"github.com/seznam/jailoc/internal/workspace"
)

// TestImportWorkspaceImageNeverModifiesDaemonImages runs the import against a
// real HTTP Docker API server that lists a stale jailoc image another HOME
// may still use, and fails on any request that could delete it. It sets
// HOME and DOCKER_HOST, so it does not run in parallel.
func TestImportWorkspaceImageNeverModifiesDaemonImages(t *testing.T) {
	const id = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	// Seed the image cache so the import does not need a docker save tarball.
	ref := vm.RootFSRef(id)
	tag := strings.TrimPrefix(id, "sha256:")
	cached := filepath.Join(home, ".cache", "jailoc-vm", "images", "erofs", "images", tag)
	if err := os.MkdirAll(cached, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cached, "image.json"), []byte(`{"layers":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var requests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.45")
			w.Header().Set("OSType", "linux")
			_, _ = w.Write([]byte("OK"))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/json"):
			// An old jailoc image, e.g. one that another HOME's workspace uses.
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"Id":       "sha256:" + strings.Repeat("b", 64),
				"RepoTags": []string{"jailoc-agent-omo:stale"},
				"Created":  time.Now().Add(-48 * time.Hour).Unix(),
				"Labels":   map[string]string{docker.ImageLabel: "1"},
			}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":           id,
				"Os":           "linux",
				"Architecture": runtime.GOARCH,
				"Size":         0,
				"RootFS":       map[string]any{"Type": "layers", "Layers": []string{}},
			})
		case r.Method == http.MethodDelete || r.Method == http.MethodPost:
			_, _ = w.Write([]byte("[]"))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(ts.URL, "http://"))

	ws := &workspace.Resolved{Name: "other-home-ws", Image: "example.test/workspace:1"}
	got, err := importWorkspaceImage(context.Background(), &config.Config{}, ws)
	if err != nil {
		t.Fatalf("importWorkspaceImage: %v", err)
	}
	if got != ref {
		t.Errorf("importWorkspaceImage() = %q, want %q", got, ref)
	}

	mu.Lock()
	defer mu.Unlock()
	for _, req := range requests {
		if strings.HasPrefix(req, http.MethodDelete+" ") || strings.HasPrefix(req, http.MethodPost+" ") || strings.HasSuffix(req, "/images/json") {
			t.Errorf("import sent Docker request %q, want only inspect requests", req)
		}
	}
	if len(requests) == 0 {
		t.Error("import sent no request to the Docker daemon")
	}
	if _, err := os.Stat(filepath.Join(home, ".cache", "jailoc-vm", "workspaces", ws.Name, "docker-images.json")); err == nil {
		t.Error("import recorded per-workspace Docker image IDs")
	}
}
