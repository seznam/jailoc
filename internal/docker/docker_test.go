package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docker/docker/api/types/build"

	"github.com/seznam/jailoc/internal/agent"
	"github.com/seznam/jailoc/internal/config"
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
