package docker

import (
	"archive/tar"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/moby/moby/api/types/build"
	dockerclient "github.com/moby/moby/client"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
	"github.com/seznam/jailoc/internal/embed"
	"github.com/seznam/jailoc/internal/workspace"
)

func TestBuildPresetImageEmptyContent(t *testing.T) {
	t.Parallel()
	for _, content := range [][]byte{nil, {}} {
		_, err := buildPresetImage(context.Background(), nil, content)
		if err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("buildPresetImage(%v) = %v, want empty content error", content, err)
		}
	}
}

func TestResolveBaseImageDockerfileLoadError(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.Dockerfile")
	_, err := ResolveBaseImage(context.Background(), &config.Config{Base: config.BaseConfig{Dockerfile: path}}, "dev")
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("ResolveBaseImage = %v, want load error for %s", err, path)
	}
}

func TestBuildOverlayImageWithoutDockerfile(t *testing.T) {
	t.Parallel()
	base := "registry.example.com/base:v1"
	tag, err := BuildOverlayImage(context.Background(), base, workspace.Resolved{Name: "test"})
	if err != nil || tag != base {
		t.Fatalf("BuildOverlayImage = %q, %v, want %q", tag, err, base)
	}
}

func TestBuildOverlayImageInvalidBaseOrSource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "missing.Dockerfile")
	for _, tc := range []struct{ base, want string }{{"", "base image is empty"}, {"base:v1", path}} {
		_, err := BuildOverlayImage(context.Background(), tc.base, workspace.Resolved{Name: "test", Dockerfile: path})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("BuildOverlayImage(%q) = %v, want %q", tc.base, err, tc.want)
		}
	}
}

func TestInjectBaseArg(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ input, want string }{
		{"FROM ${BASE}\n", "ARG BASE\nFROM ${BASE}\n"},
		{"# syntax=docker/dockerfile:1\nFROM ${BASE}\n", "# syntax=docker/dockerfile:1\nARG BASE\nFROM ${BASE}\n"},
		{"# escape=`\n\nFROM ${BASE}\n", "# escape=`\n\nARG BASE\nFROM ${BASE}\n"},
	} {
		if got := string(injectBaseArg([]byte(tc.input))); got != tc.want {
			t.Errorf("injectBaseArg(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestProxyBuildArgs(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy:8080")
	got := proxyBuildArgs()
	if got["HTTP_PROXY"] == nil || *got["HTTP_PROXY"] != "http://proxy:8080" {
		t.Fatalf("HTTP_PROXY = %v", got["HTTP_PROXY"])
	}
	if err := os.Unsetenv("HTTP_PROXY"); err != nil {
		t.Fatal(err)
	}
	if _, exists := proxyBuildArgs()["HTTP_PROXY"]; exists {
		t.Fatal("unset HTTP_PROXY was forwarded")
	}
}

func TestSetUbuntuMirror(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ mirror, want string }{
		{"", DefaultUbuntuMirror},
		{"https://mirror.example.com/ubuntu", "https://mirror.example.com/ubuntu"},
	} {
		args := map[string]*string{}
		setUbuntuMirror(args, tt.mirror)
		if got := args[ubuntuMirrorArg]; got == nil || *got != tt.want {
			t.Errorf("setUbuntuMirror(%q) = %v, want %q", tt.mirror, got, tt.want)
		}
	}
}

// TestEmbeddedDockerfilesRewriteUbuntuMirror checks that the embedded
// Dockerfiles consume ubuntuMirrorArg before their first apt-get update.
func TestEmbeddedDockerfilesRewriteUbuntuMirror(t *testing.T) {
	t.Parallel()

	for name, content := range map[string][]byte{"Dockerfile": embed.Dockerfile(), "docker.Dockerfile": embed.DockerLayer()} {
		s := string(content)
		arg := strings.Index(s, "ARG "+ubuntuMirrorArg+"\n")
		rewrite := strings.Index(s, "/etc/apt/sources.list.d/ubuntu.sources")
		update := strings.Index(s, "apt-get update")
		if arg < 0 || rewrite < arg || update < rewrite {
			t.Errorf("%s: ARG %s at %d, sources rewrite at %d, apt-get update at %d; want them in this order", name, ubuntuMirrorArg, arg, rewrite, update)
		}
	}
}

func TestContentHash(t *testing.T) {
	t.Parallel()
	content := []byte("FROM ubuntu:24.04\n")
	h1 := contentHash(content, nil)
	h2 := contentHash(content, map[string]*string{})
	if h1 != h2 {
		t.Errorf("contentHash(nil) = %q != contentHash(empty) = %q", h1, h2)
	}
	v1 := "http://proxy1:8080"
	hWithProxy := contentHash(content, map[string]*string{"HTTP_PROXY": &v1})
	if hWithProxy == h1 {
		t.Errorf("contentHash with HTTP_PROXY matches hash without proxy")
	}
	v2 := "http://proxy2:8080"
	hWithProxy2 := contentHash(content, map[string]*string{"HTTP_PROXY": &v2})
	if hWithProxy2 == hWithProxy {
		t.Errorf("contentHash did not change when proxy value changed")
	}
	// Order of keys should not affect hash
	a, b := "foo", "bar"
	m1 := map[string]*string{"A": &a, "B": &b}
	m2 := map[string]*string{"B": &b, "A": &a}
	if contentHash(content, m1) != contentHash(content, m2) {
		t.Errorf("contentHash is not deterministic with respect to map iteration order")
	}
}

func TestDockerLayerDockerfile(t *testing.T) {
	t.Parallel()

	const stages = "FROM ubuntu:24.04 AS docker\nRUN true\n"
	layer := "ARG JAILOC_CLEAN_CACHES\n" +
		"RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \\\n" +
		"    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \\\n" +
		"    command -v nft >/dev/null || (apt-get update \\\n" +
		"    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends nftables \\\n" +
		"    && if [ -n \"$JAILOC_CLEAN_CACHES\" ]; then apt-get clean && rm -rf /var/lib/apt/lists/*; fi)\n" +
		"COPY --from=docker /out/bin/ /usr/local/bin/\n" +
		"COPY --from=docker /out/cli-plugins/ /usr/local/lib/docker/cli-plugins/\n"
	head := layerHead + stages + "\nFROM ${BASE}\n"
	tests := []struct {
		user string
		want string
	}{
		{"", head + layer},
		{"root", head + layer},
		{"agent", head + "USER root\n" + layer + "USER agent\n"},
	}
	for _, tt := range tests {
		t.Run("user="+tt.user, func(t *testing.T) {
			t.Parallel()
			if got := string(dockerLayerDockerfile([]byte(stages), tt.user)); got != tt.want {
				t.Fatalf("dockerLayerDockerfile() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestAgentDockerfile(t *testing.T) {
	t.Parallel()

	claude, _ := agent.Lookup("claude")
	install := "ARG JAILOC_CLEAN_CACHES\n" +
		"RUN --mount=type=cache,target=/root/.npm npm install -g --cache /root/.npm --allow-scripts='@anthropic-ai/claude-code' '@anthropic-ai/claude-code@" + claude.Version + "' \\\n" +
		"    && if [ -n \"$JAILOC_CLEAN_CACHES\" ]; then rm -rf /root/.npm; fi\n"
	tests := []struct {
		user string
		want string
	}{
		{"", layerHead + "FROM ${BASE}\n" + install},
		{"root", layerHead + "FROM ${BASE}\n" + install},
		{"0:0", layerHead + "FROM ${BASE}\n" + install},
		{"agent", layerHead + "FROM ${BASE}\nUSER root\n" + install + "USER agent\n"},
		{"1000:1000", layerHead + "FROM ${BASE}\nUSER root\n" + install + "USER 1000:1000\n"},
	}
	for _, tt := range tests {
		t.Run("user="+tt.user, func(t *testing.T) {
			t.Parallel()
			if got := string(agentDockerfile(claude, tt.user)); got != tt.want {
				t.Fatalf("agentDockerfile() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestAgentDockerfileRelease(t *testing.T) {
	t.Parallel()

	h := agent.Harness{
		Binary:  "cli",
		Version: "2.0.0",
		Release: &agent.Release{
			Repo:    "owner/cli",
			Asset:   "cli_linux_{arch}.tar.gz",
			Binary:  "native",
			Version: "2.0.0",
			SHA256:  map[string]string{"x64": "aaa", "arm64": "bbb"},
		},
	}
	install := "RUN set -eu; \\\n" +
		"    case \"$(uname -m)\" in \\\n" +
		"      x86_64) arch=x64 sha=aaa ;; \\\n" +
		"      aarch64) arch=arm64 sha=bbb ;; \\\n" +
		"      *) echo \"unsupported architecture $(uname -m)\" >&2; exit 1 ;; \\\n" +
		"    esac; \\\n" +
		"    tmp=$(mktemp -d); \\\n" +
		"    curl -fsSL -o \"$tmp/release.tar.gz\" \"https://github.com/owner/cli/releases/download/2.0.0/cli_linux_${arch}.tar.gz\"; \\\n" +
		"    echo \"${sha}  $tmp/release.tar.gz\" | sha256sum -c -; \\\n" +
		"    tar -xzf \"$tmp/release.tar.gz\" -C \"$tmp\" 'native'; \\\n" +
		"    install -m 0755 \"$tmp/native\" '/usr/local/bin/cli'; \\\n" +
		"    rm -rf \"$tmp\"\n"
	want := layerHead + "FROM ${BASE}\nUSER root\n" + install + "USER agent\n"
	if got := string(agentDockerfile(h, "agent")); got != want {
		t.Fatalf("agentDockerfile() =\n%s\nwant\n%s", got, want)
	}
}

func TestForBuilder(t *testing.T) {
	t.Parallel()

	dockerfile := "FROM x\n" +
		"RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \\\n" +
		"    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \\\n" +
		"    apt-get update\n" +
		"RUN --mount=type=cache,target=/root/.npm npm install -g x \\\n" +
		"    && true\n" +
		"RUN --mount=type=secret,id=s cat /run/secrets/s\n"
	tests := []struct {
		name      string
		version   build.BuilderVersion
		want      string
		wantClean bool
	}{
		{"buildkit", build.BuilderBuildKit, dockerfile, false},
		{"classic", build.BuilderV1, "FROM x\n" +
			"RUN apt-get update\n" +
			"RUN npm install -g x \\\n" +
			"    && true\n" +
			"RUN --mount=type=secret,id=s cat /run/secrets/s\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]*string{}
			if got := string(forBuilder(tt.version, []byte(dockerfile), args)); got != tt.want {
				t.Fatalf("forBuilder() =\n%s\nwant\n%s", got, tt.want)
			}
			if v, ok := args[cleanCachesArg]; ok != tt.wantClean || (ok && *v != "1") {
				t.Fatalf("args[%s] = %v, %v, want set %v", cleanCachesArg, v, ok, tt.wantClean)
			}
		})
	}
}

// fakeImage is an image of a fakeEngine.
type fakeImage struct {
	user    string
	env     []string
	onBuild []string
	// nonDebian images have no /etc/debian_version.
	nonDebian bool
}

// fakeEngine is a fake Docker daemon that knows a fixed set of images, fails
// every pull and reports BuildKit as its builder when buildKit is set.
type fakeEngine struct {
	buildKit bool
	images   map[string]fakeImage

	mu         sync.Mutex
	requests   []string
	containers map[string]string // container ID -> image reference
	// removals records, per DELETE /containers/{id}, whether it asked for the
	// container's anonymous volumes to be removed too (v=1).
	removals []bool
}

func (e *fakeEngine) handler(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	e.requests = append(e.requests, r.Method+" "+r.URL.Path)
	e.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if strings.HasPrefix(path, "/v1.") {
		path = path[strings.Index(path[1:], "/")+1:]
	}
	switch {
	case path == "/_ping":
		w.Header().Set("API-Version", "1.52")
		w.Header().Set("OSType", "linux")
		if e.buildKit {
			w.Header().Set("Builder-Version", "2")
		}
		_, _ = w.Write([]byte("OK"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		ref := strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		img, ok := e.images[ref]
		if !ok {
			http.Error(w, `{"message":"No such image: `+ref+`"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":           hexID("a"),
			"Os":           "linux",
			"Architecture": runtime.GOARCH,
			"Config":       map[string]any{"User": img.user, "Env": img.env, "OnBuild": img.onBuild},
		})
	case r.Method == http.MethodPost && path == "/containers/create":
		e.createContainer(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/archive"):
		e.containerArchive(w, strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/archive"))
	case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
		e.mu.Lock()
		e.removals = append(e.removals, r.URL.Query().Get("v") == "1")
		e.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodPost && path == "/images/create":
		http.Error(w, `{"message":"pull access denied, repository does not exist"}`, http.StatusInternalServerError)
	default:
		http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
	}
}

// createContainer creates a fake container of a known image. Its ID is
// "fake-container-" plus the image reference, with path separators replaced.
func (e *fakeEngine) createContainer(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image string
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, `{"message":"bad request"}`, http.StatusBadRequest)
		return
	}
	if _, ok := e.images[body.Image]; !ok {
		http.Error(w, `{"message":"No such image: `+body.Image+`"}`, http.StatusNotFound)
		return
	}
	id := "fake-container-" + strings.NewReplacer("/", "_", ":", "_", "@", "_").Replace(body.Image)
	e.mu.Lock()
	if e.containers == nil {
		e.containers = make(map[string]string)
	}
	e.containers[id] = body.Image
	e.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Warnings": []string{}})
}

// containerArchive serves /etc/debian_version of a fake container, which
// non-Debian images lack.
func (e *fakeEngine) containerArchive(w http.ResponseWriter, id string) {
	e.mu.Lock()
	ref, ok := e.containers[id]
	e.mu.Unlock()
	if !ok {
		http.Error(w, `{"message":"No such container: `+id+`"}`, http.StatusNotFound)
		return
	}
	if e.images[ref].nonDebian {
		http.Error(w, `{"message":"Could not find the file /etc/debian_version in container `+id+`"}`, http.StatusNotFound)
		return
	}
	const content = "12.0\n"
	stat, _ := json.Marshal(map[string]any{"name": "debian_version", "size": len(content), "mode": 0o644, "mtime": "2024-01-01T00:00:00Z", "linkTarget": ""})
	w.Header().Set("X-Docker-Container-Path-Stat", base64.StdEncoding.EncodeToString(stat))
	w.Header().Set("Content-Type", "application/x-tar")
	tw := tar.NewWriter(w)
	_ = tw.WriteHeader(&tar.Header{Name: "debian_version", Mode: 0o644, Size: int64(len(content))})
	_, _ = tw.Write([]byte(content))
	_ = tw.Close()
}

// start serves e and returns its address as a Docker host.
func (e *fakeEngine) start(t *testing.T) string {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(e.handler))
	t.Cleanup(ts.Close)
	return "tcp://" + strings.TrimPrefix(ts.URL, "http://")
}

func (e *fakeEngine) client(t *testing.T) *dockerclient.Client {
	t.Helper()
	cli, err := dockerclient.New(dockerclient.WithHost(e.start(t)), dockerclient.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

// requested returns the requests that start with prefix, as "METHOD path".
func (e *fakeEngine) requested(prefix string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []string
	for _, r := range e.requests {
		if strings.Contains(r, prefix) {
			out = append(out, r)
		}
	}
	return out
}

func testBuildCA(t *testing.T) *BuildCA {
	t.Helper()
	ca, err := NewBuildCA([]byte("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"))
	if err != nil || ca == nil {
		t.Fatalf("NewBuildCA = %v, %v", ca, err)
	}
	return ca
}

// TestEnsureBuildImage points the Docker environment at a fake daemon, so it
// does not run in parallel.
func TestEnsureBuildImage(t *testing.T) {
	e := &fakeEngine{}
	t.Setenv("DOCKER_HOST", e.start(t))
	t.Setenv("DOCKER_API_VERSION", "1.52")
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")

	t.Run("with a CA bundle a pull failure points at registries", func(t *testing.T) {
		err := EnsureBuildImage(context.Background(), "registry.example.com/base:v1", testBuildCA(t))
		if err == nil {
			t.Fatal("EnsureBuildImage = nil, want error")
		}
		for _, want := range []string{"registry.example.com/base:v1", "builder VM", "registry", "ca_bundle = false", "pull access denied"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
	})

	t.Run("without a CA bundle the error is the pull error", func(t *testing.T) {
		err := EnsureBuildImage(context.Background(), "registry.example.com/base:v1", nil)
		if err == nil {
			t.Fatal("EnsureBuildImage = nil, want error")
		}
		if strings.Contains(err.Error(), "builder VM") || strings.Contains(err.Error(), "ca_bundle") {
			t.Errorf("error %q has CA guidance without a CA bundle", err)
		}
		if !strings.Contains(err.Error(), "pull image registry.example.com/base:v1") {
			t.Errorf("error %q is not the pull error", err)
		}
	})

	t.Run("prepareCADockerfile falls back to it for a missing image", func(t *testing.T) {
		_, err := prepareCADockerfile(context.Background(), e.client(t), []byte("FROM registry.example.com/base:v1\n"), nil, testBuildCA(t))
		if err == nil || !strings.Contains(err.Error(), "builder VM") {
			t.Fatalf("prepareCADockerfile = %v, want the builder VM guidance", err)
		}
	})
}

func TestBuildImagesRejectClassicBuilderWithCA(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	ctx := context.Background()

	t.Run("embedded", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{}
		err := buildEmbeddedImage(ctx, e.client(t), "jailoc-base:embedded"+ca.tagSuffix(), "", ca)
		if err == nil || !strings.Contains(err.Error(), "requires BuildKit") {
			t.Fatalf("buildEmbeddedImage = %v, want the BuildKit error", err)
		}
		if got := e.requested("/images/"); len(got) != 0 {
			t.Errorf("rejected build still touched images: %v", got)
		}
	})

	t.Run("preset", func(t *testing.T) {
		t.Parallel()
		e := &fakeEngine{}
		_, err := buildPresetImage(ctx, e.client(t), []byte("FROM ubuntu:26.04\n"), ca)
		if err == nil || !strings.Contains(err.Error(), "requires BuildKit") {
			t.Fatalf("buildPresetImage = %v, want the BuildKit error", err)
		}
		if got := e.requested("/images/"); len(got) != 0 {
			t.Errorf("rejected build still touched images: %v", got)
		}
	})
}

func TestOptionalBuildCAVariadicEntryPoints(t *testing.T) {
	t.Parallel()
	ca := testBuildCA(t)
	ctx := context.Background()
	if _, err := ResolveBaseImage(ctx, nil, "dev", ca, ca); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("ResolveBaseImage with two CAs = %v, want error", err)
	}
	ws := workspace.Resolved{Name: "x", Dockerfile: "/nonexistent/Dockerfile"}
	if _, err := BuildOverlayImage(ctx, "base", ws, ca, ca); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("BuildOverlayImage with two CAs = %v, want error", err)
	}
	if _, err := BuildAgentImage(ctx, "base", agent.Harness{}, ca, ca); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("BuildAgentImage with two CAs = %v, want error", err)
	}
	if _, err := BuildDockerImage(ctx, "base", "", ca, ca); err == nil || !strings.Contains(err.Error(), "at most one") {
		t.Errorf("BuildDockerImage with two CAs = %v, want error", err)
	}
}
