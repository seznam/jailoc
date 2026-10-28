package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestAgentDockerfile(t *testing.T) {
	t.Parallel()

	claude, _ := agent.Lookup("claude")
	install := "RUN npm install -g --allow-scripts='@anthropic-ai/claude-code' '@anthropic-ai/claude-code@" + claude.Version + "' && npm cache clean --force\n"
	tests := []struct {
		user string
		want string
	}{
		{"", "ARG BASE\nFROM ${BASE}\n" + install},
		{"root", "ARG BASE\nFROM ${BASE}\n" + install},
		{"0:0", "ARG BASE\nFROM ${BASE}\n" + install},
		{"agent", "ARG BASE\nFROM ${BASE}\nUSER root\n" + install + "USER agent\n"},
		{"1000:1000", "ARG BASE\nFROM ${BASE}\nUSER root\n" + install + "USER 1000:1000\n"},
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
