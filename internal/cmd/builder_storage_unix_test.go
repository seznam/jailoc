//go:build darwin || linux

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/docker"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestBuilderDiskSize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		builder   string
		isolated  bool
		docker    string
		want      int64
		wantError string
	}{
		{name: "shared default", docker: "50g", want: 20 << 30},
		{name: "shared configured", builder: "40g", docker: "50g", want: 40 << 30},
		{name: "shared megabytes", builder: "2048m", want: 2 << 30},
		{name: "shared ignores invalid docker_disk", builder: "30g", docker: "bogus", want: 30 << 30},
		{name: "shared exactly 1GiB", builder: "1g", want: 1 << 30},
		{name: "shared below 1GiB", builder: "1023m", wantError: "builder_disk"},
		{name: "shared words", builder: "big", wantError: "builder_disk"},
		{name: "shared zero", builder: "0g", wantError: "builder_disk"},
		{name: "shared overflow", builder: "9999999999999g", wantError: "builder_disk"},
		{name: "isolated uses docker_disk", isolated: true, builder: "40g", docker: "30g", want: 30 << 30},
		{name: "isolated ignores invalid builder_disk", isolated: true, builder: "bogus", docker: "30g", want: 30 << 30},
		{name: "isolated invalid docker_disk", isolated: true, builder: "40g", docker: "512m", wantError: "docker_disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{VM: config.VMConfig{BuilderDisk: tt.builder}}
			ws := &workspace.Resolved{Name: "ws", IsolatedBuild: tt.isolated, DockerDisk: tt.docker}
			got, err := builderDiskSize(cfg, ws)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("builderDiskSize error = %v, want %q", err, tt.wantError)
				}
				if tt.wantError == "builder_disk" && strings.Contains(err.Error(), "docker_disk") {
					t.Fatalf("builderDiskSize error = %v, must name builder_disk only", err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("builderDiskSize = %d, %v, want %d", got, err, tt.want)
			}
		})
	}
}

func TestVMDockerDiskSizeErrorsNameDockerDisk(t *testing.T) {
	t.Parallel()
	tests := []struct{ size, want string }{
		{size: "", want: "empty docker_disk"},
		{size: "0g", want: `invalid docker_disk "0g"`},
		{size: "9999999999999g", want: `docker_disk "9999999999999g" is too large`},
		{size: "1023m", want: `docker_disk "1023m" is below the 1 GiB minimum`},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if _, err := vmDockerDiskSize(tt.size); err == nil || err.Error() != tt.want {
				t.Fatalf("vmDockerDiskSize(%q) error = %v, want %q", tt.size, err, tt.want)
			}
		})
	}
}

func TestBuilderKeepIDs(t *testing.T) {
	t.Parallel()
	cache := t.TempDir()
	images := filepath.Join(cache, "erofs", "images")
	for _, name := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64), ".staging-123"} {
		if err := os.MkdirAll(filepath.Join(images, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := builderKeepIDs(cache)
	if err != nil {
		t.Fatalf("builderKeepIDs: %v", err)
	}
	if len(got) != 2 || !got[strings.Repeat("a", 64)] || !got[strings.Repeat("b", 64)] {
		t.Fatalf("builderKeepIDs = %v, want the two image tags without staging entries", got)
	}

	got, err = builderKeepIDs(filepath.Join(t.TempDir(), "missing"))
	if err != nil || len(got) != 0 {
		t.Fatalf("builderKeepIDs of an empty cache = %v, %v, want no IDs and no error", got, err)
	}
}

func TestWithBuilderMaintenance(t *testing.T) {
	t.Parallel()
	errBuild := errors.New("build failed")
	tests := []struct {
		name       string
		cancelRun  bool
		runErr     error
		wantPhases []string
		wantRef    string
	}{
		{name: "success", wantPhases: []string{"before build", "build", "after build"}, wantRef: "ref"},
		{name: "build failure still maintains", runErr: errBuild, wantPhases: []string{"before build", "build", "after build"}},
		{name: "cancellation skips the maintenance after", cancelRun: true, runErr: context.Canceled, wantPhases: []string{"before build", "build"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var phases []string
			ref, err := withBuilderMaintenance(ctx, func(_ context.Context, phase string) {
				phases = append(phases, phase)
			}, func() (string, error) {
				phases = append(phases, "build")
				if tt.cancelRun {
					cancel()
				}
				if tt.runErr != nil {
					return "", tt.runErr
				}
				return "ref", nil
			})
			if !errors.Is(err, tt.runErr) {
				t.Fatalf("error = %v, want the build error %v unchanged", err, tt.runErr)
			}
			if ref != tt.wantRef {
				t.Errorf("ref = %q, want %q", ref, tt.wantRef)
			}
			if !slices.Equal(phases, tt.wantPhases) {
				t.Errorf("phases = %v, want %v", phases, tt.wantPhases)
			}
		})
	}

	t.Run("cancelled before the build skips all maintenance", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var phases []string
		_, _ = withBuilderMaintenance(ctx, func(_ context.Context, phase string) { phases = append(phases, phase) }, func() (string, error) {
			phases = append(phases, "build")
			return "", ctx.Err()
		})
		if !slices.Equal(phases, []string{"build"}) {
			t.Errorf("phases = %v, want only the build", phases)
		}
	})
}

// TestMaintainBuilderDaemon runs the maintenance against a real HTTP Docker
// API server. It sets HOME and DOCKER_HOST, so it does not run in parallel.
func TestMaintainBuilderDaemon(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "1.52")
	kept := strings.Repeat("a", 64)
	if err := os.MkdirAll(filepath.Join(home, ".cache", "jailoc-vm", "images", "erofs", "images", kept), 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour).Unix()

	var mu sync.Mutex
	var requests []string
	var pruneQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/build/prune"):
			mu.Lock()
			pruneQuery = r.URL.RawQuery
			mu.Unlock()
			_, _ = w.Write([]byte(`{"CachesDeleted":["c1"],"SpaceReclaimed":3221225472}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/images/json"):
			_, _ = w.Write([]byte(`[` +
				`{"Id":"sha256:` + kept + `","RepoTags":["jailoc-agent-omo:kept"],"Created":` + strconv.FormatInt(old, 10) + `,"Labels":{"` + docker.ImageLabel + `":"1"}},` +
				`{"Id":"sha256:` + strings.Repeat("c", 64) + `","RepoTags":["jailoc-agent-omo:stale"],"Created":` + strconv.FormatInt(old, 10) + `,"Labels":{"` + docker.ImageLabel + `":"1"}}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			// Each image has a layer of its own, so none is another's ancestor.
			_ = json.NewEncoder(w).Encode(map[string]any{"RootFS": map[string]any{"Type": "layers", "Layers": []string{strings.Split(r.URL.Path, "/")[3][7:12]}}})
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`[]`))
		default:
			http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(ts.URL, "http://"))

	var stdout, stderr bytes.Buffer
	maintainBuilderDaemon(context.Background(), 20<<30, "after build", &stdout, &stderr)

	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want no warning", stderr.String())
	}
	for _, want := range []string{"after build", "3 GiB", "removed 1 unused jailoc image"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q, want %q", stdout.String(), want)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(pruneQuery, "max-used-space=10737418240") || !strings.Contains(pruneQuery, "min-free-space=4294967296") {
		t.Errorf("build prune query = %q, want the half and fifth of the 20 GiB disk", pruneQuery)
	}
	var deletes []string
	for _, r := range requests {
		if strings.HasPrefix(r, http.MethodDelete+" ") {
			deletes = append(deletes, r)
		}
	}
	if len(deletes) != 1 || !strings.HasSuffix(deletes[0], "/images/jailoc-agent-omo:stale") {
		t.Errorf("DELETE requests = %v, want only the stale image: the one in the VM image cache is kept", deletes)
	}
}

func TestMaintainBuilderDaemonWarnsOnFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "1.52")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"daemon broken"}`, http.StatusInternalServerError)
	}))
	t.Cleanup(ts.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(ts.URL, "http://"))

	var stdout, stderr bytes.Buffer
	maintainBuilderDaemon(context.Background(), 20<<30, "before build", &stdout, &stderr)
	for _, want := range []string{"WARNING", "builder maintenance", "before build", "daemon broken"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr = %q, want %q", stderr.String(), want)
		}
	}
}
