package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateComposeSinglePath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "alpha",
		Port:             4111,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/Users/test/work/project"},
		Env:            nil,
		CPU:            2.0,
		Memory:         "4g",
		UseDataVolume:  true,
		UseCacheVolume: true,
		ExposePort:     true,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "name: jailoc-alpha")
	assertContains(t, rendered, "image: ghcr.io/seznam/jailoc:test")
	assertContains(t, rendered, "- \"127.0.0.1:4111:4096\"")
	assertContains(t, rendered, "- /Users/test/work/project:/Users/test/work/project")
	assertContains(t, rendered, "OPENCODE_SERVER_PASSWORD=${OPENCODE_SERVER_PASSWORD}")
	assertContains(t, rendered, "opencode-data-alpha")
	assertContains(t, rendered, "opencode-cache-alpha")
	assertContains(t, rendered, "working_dir: /Users/test/work/project")
	assertContains(t, rendered, "healthcheck:")
	assertContains(t, rendered, "$$OPENCODE_SERVER_PASSWORD")
	assertContains(t, rendered, "/global/health")
	assertContains(t, rendered, "entrypoint.sh:/usr/local/bin/entrypoint.sh:ro")
}

func TestGenerateComposeMultiplePaths(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "beta",
		Port:          4222,
		Image:         "ghcr.io/seznam/jailoc:latest",
		Paths: []string{
			"/repos/api",
			"/repos/web-app",
		},
		Env:    nil,
		CPU:    2.0,
		Memory: "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- /repos/api:/repos/api")
	assertContains(t, rendered, "- /repos/web-app:/repos/web-app")
	assertContains(t, rendered, "working_dir: /repos/api")
}

func TestGenerateComposePasswordUsesEnvSubstitution(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "gamma",
		Port:          4333,
		Image:         "ghcr.io/seznam/jailoc:dev",
		Paths:         []string{"/tmp/work"},
		Env:           nil,
		CPU:           2.0,
		Memory:        "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "OPENCODE_SERVER_PASSWORD=${OPENCODE_SERVER_PASSWORD}")
}

func TestGenerateComposeVolumeNamesIncludeWorkspaceName(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:  "delta",
		Port:           4444,
		Image:          "ghcr.io/seznam/jailoc:main",
		Paths:          []string{"/tmp/repo"},
		Env:            nil,
		CPU:            2.0,
		Memory:         "4g",
		UseDataVolume:  true,
		UseCacheVolume: true,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- opencode-data-delta:/home/agent/.local/share/opencode")
	assertContains(t, rendered, "- opencode-cache-delta:/home/agent/.cache")
	assertContains(t, rendered, "opencode-data-delta:")
	assertContains(t, rendered, "opencode-cache-delta:")
}

func TestWriteComposeFileHappyPath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "test-ws",
		Port:             4500,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/workspace"},
		Env: nil,
		CPU:              2.0,
		Memory:           "4g",
		ExposePort:       true,
	}

	destPath := filepath.Join(t.TempDir(), "docker-compose.yml")

	err := WriteComposeFile(params, destPath)
	if err != nil {
		t.Fatalf("WriteComposeFile returned error: %v", err)
	}

	content, err := os.ReadFile(destPath) //nolint:gosec
	if err != nil {
		t.Fatalf("failed to read written file: %v", err)
	}

	if len(content) == 0 {
		t.Fatal("written compose file is empty")
	}

	rendered := string(content)
	assertContains(t, rendered, "name: jailoc-test-ws")
	assertContains(t, rendered, "image: ghcr.io/seznam/jailoc:test")
	assertContains(t, rendered, "- \"127.0.0.1:4500:4096\"")

	stat, err := os.Stat(destPath)
	if err != nil {
		t.Fatalf("failed to stat file: %v", err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("expected file permissions 0o600, got %#o", stat.Mode().Perm())
	}
}

func TestWriteComposeFileErrorPath(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test-ws",
		Port:          4500,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/workspace"},
		Env:           nil,
		CPU:           2.0,
		Memory:        "4g",
	}

	destPath := "/nonexistent/directory/docker-compose.yml"

	err := WriteComposeFile(params, destPath)
	if err == nil {
		t.Fatal("expected WriteComposeFile to return error for invalid destination, got nil")
	}

	if !strings.Contains(err.Error(), "write compose file") {
		t.Fatalf("expected error message to contain 'write compose file', got: %v", err)
	}
}

func TestGenerateComposeExposePortFalseOmitsPorts(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "noport",
		Port:          4111,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/workspace"},
		CPU:           2.0,
		Memory:        "4g",
		ExposePort:    false,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	if strings.Contains(rendered, "ports:") {
		t.Fatal("expected no ports: section when ExposePort is false")
	}
	if strings.Contains(rendered, "127.0.0.1") {
		t.Fatal("expected no 127.0.0.1 binding when ExposePort is false")
	}
}

func assertContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected rendered compose to contain %q, got:\n%s", needle, haystack)
	}
}

func assertNotContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Fatalf("expected rendered compose NOT to contain %q, got:\n%s", needle, haystack)
	}
}

func TestGenerateComposeSSHAuthSock(t *testing.T) {
	t.Parallel()

	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "ssh-test",
			Port:             4700,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			SSHAuthSock: "/run/host-services/ssh-auth.sock",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "/run/host-services/ssh-auth.sock:/run/ssh-agent.sock")
		assertContains(t, rendered, "SSH_AUTH_SOCK=/run/ssh-agent.sock")
	})

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "no-ssh-test",
			Port:             4701,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			SSHAuthSock: "",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		if strings.Contains(rendered, "ssh-agent.sock") {
			t.Fatalf("expected no SSH agent socket mount when disabled, got:\n%s", rendered)
		}
		if strings.Contains(rendered, "SSH_AUTH_SOCK") {
			t.Fatalf("expected no SSH_AUTH_SOCK env when disabled, got:\n%s", rendered)
		}
	})
}

func TestGenerateComposeGitConfig(t *testing.T) {
	t.Parallel()

	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "git-test",
			Port:             4702,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			GitConfig: "/home/user/.gitconfig",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "/home/user/.gitconfig:/home/agent/.gitconfig:ro")
	})

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "no-git-test",
			Port:             4703,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			GitConfig: "",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		if strings.Contains(rendered, ".gitconfig") {
			t.Fatalf("expected no gitconfig mount when disabled, got:\n%s", rendered)
		}
	})
}

func TestGenerateComposeSSHKnownHosts(t *testing.T) {
	t.Parallel()

	t.Run("enabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "known-hosts-test",
			Port:             4704,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			SSHKnownHosts: "/home/user/.ssh/known_hosts",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "/home/user/.ssh/known_hosts:/home/agent/.ssh/known_hosts:ro")
	})

	t.Run("disabled", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:    "no-known-hosts-test",
			Port:             4705,
			Image:            "ghcr.io/seznam/jailoc:test",
			Paths:            []string{"/tmp/work"},
			SSHKnownHosts: "",
			CPU:              2.0,
			Memory:           "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		if strings.Contains(rendered, "known_hosts") {
			t.Fatalf("expected no known_hosts mount when disabled, got:\n%s", rendered)
		}
	})
}

func TestGenerateComposeAllSSHGitEnabled(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "all-ssh-git-test",
		Port:             4706,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		SSHAuthSock:   "/run/host-services/ssh-auth.sock",
		GitConfig:        "/home/user/.gitconfig",
		SSHKnownHosts:    "/home/user/.ssh/known_hosts",
		CPU:              2.0,
		Memory:           "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)
	assertContains(t, rendered, "/run/host-services/ssh-auth.sock:/run/ssh-agent.sock")
	assertContains(t, rendered, "SSH_AUTH_SOCK=/run/ssh-agent.sock")
	assertContains(t, rendered, "/home/user/.gitconfig:/home/agent/.gitconfig:ro")
	assertContains(t, rendered, "/home/user/.ssh/known_hosts:/home/agent/.ssh/known_hosts:ro")
}

func TestGenerateComposeEnv(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "env-test",
		Port:             4600,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		Env:    []string{"MY_VAR=hello", "OTHER=world"},
		CPU:              2.0,
		Memory:           "4g",
		EnableDocker:     true,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// Check that user env vars are present (double-quoted in YAML)
	assertContains(t, rendered, `- "MY_VAR=hello"`)
	assertContains(t, rendered, `- "OTHER=world"`)

	// Check that system env vars are still present
	assertContains(t, rendered, "- OPENCODE_LOG=debug")
	assertContains(t, rendered, "- DOCKER_HOST=tcp://dind:2376")
	assertContains(t, rendered, "- DOCKER_TLS_VERIFY=1")
}

func TestGenerateComposeEmptyEnv(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "empty-env-test",
		Port:             4700,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		Env:    nil,
		CPU:              2.0,
		Memory:           "4g",
		EnableDocker:     true,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// Check that system env vars are still present
	assertContains(t, rendered, "- OPENCODE_LOG=debug")
	assertContains(t, rendered, "- DOCKER_HOST=tcp://dind:2376")
	assertContains(t, rendered, "- DOCKER_TLS_VERIFY=1")

	// Verify no extra blank entries are added when Env is nil
	lines := strings.Split(rendered, "\n")
	environmentIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "environment:") {
			environmentIdx = i
			break
		}
	}
	if environmentIdx >= 0 {
		afterEnv := lines[environmentIdx+1]
		if !strings.HasPrefix(afterEnv, "      -") {
			t.Errorf("expected environment entry starting with '      -', got: %q", afterEnv)
		}
	}
}

func TestGenerateComposeJailocEnvVars(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName:    "test-jailoc",
		Port:             4800,
		Image:            "ghcr.io/seznam/jailoc:test",
		Paths:            []string{"/tmp/work"},
		Env:              nil,
		CPU:              2.0,
		Memory:           "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	assertContains(t, rendered, "- JAILOC=1")
	assertContains(t, rendered, "- JAILOC_WORKSPACE=test-jailoc")
}

func TestComposeResourceLimits(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test",
		Port:          4096,
		Image:         "ubuntu:22.04",
		Paths:         []string{"/data/workspace"},
		CPU:           2.0,
		Memory:        "4g",
	}

	rendered, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose failed: %v", err)
	}

	assertContains(t, string(rendered), `mem_limit: "4g"`)
	assertContains(t, string(rendered), `memswap_limit: "4g"`)
	assertContains(t, string(rendered), "cpus: 2")
	assertContains(t, string(rendered), "mem_reservation: 512m")
	assertContains(t, string(rendered), "pids_limit: 256")
}

func TestComposeCustomResourceLimits(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test",
		Port:          4096,
		Image:         "ubuntu:22.04",
		Paths:         []string{"/data/workspace"},
		CPU:           4.0,
		Memory:        "8g",
	}

	rendered, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose failed: %v", err)
	}

	assertContains(t, string(rendered), `mem_limit: "8g"`)
	assertContains(t, string(rendered), `memswap_limit: "8g"`)
	assertContains(t, string(rendered), "cpus: 4")
	// Old values must NOT be present
	if strings.Contains(string(rendered), `mem_limit: "4g"`) {
		t.Error(`expected custom mem_limit: "8g", found old value mem_limit: "4g"`)
	}
}

func TestComposeResourceLimitsFractionalCPU(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test",
		Port:          4096,
		Image:         "ubuntu:22.04",
		Paths:         []string{"/data/workspace"},
		CPU:           1.5,
		Memory:        "512m",
	}

	rendered, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose failed: %v", err)
	}

	assertContains(t, string(rendered), "cpus: 1.5")
	assertContains(t, string(rendered), `mem_limit: "512m"`)
	assertContains(t, string(rendered), `memswap_limit: "512m"`)
}

func TestComposeHealthCheckTimings(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "test",
		Port:          4096,
		Image:         "ubuntu:22.04",
		Paths:         []string{"/data/workspace"},
		CPU:           2.0,
		Memory:        "4g",
	}

	rendered, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose failed: %v", err)
	}

	assertContains(t, string(rendered), "interval: 30s")
	assertContains(t, string(rendered), "timeout: 10s")
	assertContains(t, string(rendered), "retries: 5")
	assertContains(t, string(rendered), "start_period: 60s")

	// Old hardcoded values must not be present
	if strings.Contains(string(rendered), "interval: 10s") {
		t.Error("old health check interval 10s still present")
	}
	if strings.Contains(string(rendered), "timeout: 5s") {
		t.Error("old health check timeout 5s still present")
	}
	if strings.Contains(string(rendered), "retries: 3\n") {
		t.Error("old health check retries: 3 still present")
	}
}

func TestGenerateComposeNamedVolumeOverriddenByMount(t *testing.T) {
	t.Parallel()

	t.Run("data volume overridden", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName: "vol-override",
			Port:          4900,
			Image:         "ghcr.io/seznam/jailoc:test",
			Paths:         []string{"/tmp/work"},
			Mounts: []string{
				"/host/data:/home/agent/.local/share/opencode:rw",
			},
			UseDataVolume:  false,
			UseCacheVolume: true,
			CPU:            2.0,
			Memory:         "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "/host/data:/home/agent/.local/share/opencode:rw")
		if strings.Contains(rendered, "opencode-data-vol-override:/home/agent/.local/share/opencode") {
			t.Fatalf("expected named data volume to be absent when overridden by mount, got:\n%s", rendered)
		}
		// Cache volume should still be present
		assertContains(t, rendered, "opencode-cache-vol-override:/home/agent/.cache")
	})

	t.Run("cache volume overridden", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName: "cache-override",
			Port:          4901,
			Image:         "ghcr.io/seznam/jailoc:test",
			Paths:         []string{"/tmp/work"},
			Mounts: []string{
				"/host/cache:/home/agent/.cache:rw",
			},
			UseDataVolume:  true,
			UseCacheVolume: false,
			CPU:            2.0,
			Memory:         "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "/host/cache:/home/agent/.cache:rw")
		if strings.Contains(rendered, "opencode-cache-cache-override:/home/agent/.cache") {
			t.Fatalf("expected named cache volume to be absent when overridden by mount, got:\n%s", rendered)
		}
		// Data volume should still be present
		assertContains(t, rendered, "opencode-data-cache-override:/home/agent/.local/share/opencode")
	})

	t.Run("both volumes overridden", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName: "both-override",
			Port:          4902,
			Image:         "ghcr.io/seznam/jailoc:test",
			Paths:         []string{"/tmp/work"},
			Mounts: []string{
				"/host/data:/home/agent/.local/share/opencode:rw",
				"/host/cache:/home/agent/.cache:rw",
			},
			UseDataVolume:  false,
			UseCacheVolume: false,
			CPU:            2.0,
			Memory:         "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		if strings.Contains(rendered, "opencode-data-both-override") {
			t.Fatalf("expected no named data volume, got:\n%s", rendered)
		}
		if strings.Contains(rendered, "opencode-cache-both-override") {
			t.Fatalf("expected no named cache volume, got:\n%s", rendered)
		}
	})

	t.Run("default volumes present", func(t *testing.T) {
		t.Parallel()
		params := ComposeParams{
			WorkspaceName:  "default-vols",
			Port:           4903,
			Image:          "ghcr.io/seznam/jailoc:test",
			Paths:          []string{"/tmp/work"},
			UseDataVolume:  true,
			UseCacheVolume: true,
			CPU:            2.0,
			Memory:         "4g",
		}

		out, err := GenerateCompose(params)
		if err != nil {
			t.Fatalf("GenerateCompose returned error: %v", err)
		}

		rendered := string(out)
		assertContains(t, rendered, "opencode-data-default-vols:/home/agent/.local/share/opencode")
		assertContains(t, rendered, "opencode-cache-default-vols:/home/agent/.cache")
	})
}

func TestMountsContainTarget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		mounts        []string
		containerPath string
		want          bool
	}{
		{
			name:          "match",
			mounts:        []string{"/host/data:/home/agent/.local/share/opencode:rw"},
			containerPath: "/home/agent/.local/share/opencode",
			want:          true,
		},
		{
			name:          "no match",
			mounts:        []string{"/host/data:/home/agent/.config/opencode:ro"},
			containerPath: "/home/agent/.local/share/opencode",
			want:          false,
		},
		{
			name:          "empty mounts",
			mounts:        nil,
			containerPath: "/home/agent/.cache",
			want:          false,
		},
		{
			name:          "partial path no match",
			mounts:        []string{"/host:/home/agent/.cache/subdir:rw"},
			containerPath: "/home/agent/.cache",
			want:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := MountsContainTarget(tt.mounts, tt.containerPath)
			if got != tt.want {
				t.Errorf("MountsContainTarget(%v, %q) = %v, want %v", tt.mounts, tt.containerPath, got, tt.want)
			}
		})
	}
}

func TestGenerateComposeMountsFromParams(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "mounts-test",
		Port:          4800,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/work"},
		Mounts: []string{
			"/home/user/.config/opencode:/home/agent/.config/opencode:ro",
			"/home/user/.agents:/home/agent/.agents:ro",
		},
		CPU:    2.0,
		Memory: "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)
	assertContains(t, rendered, "/home/user/.config/opencode:/home/agent/.config/opencode:ro")
	assertContains(t, rendered, "/home/user/.agents:/home/agent/.agents:ro")

	if strings.Contains(rendered, "${HOME}/.config/opencode:/home/agent/.config/opencode:ro") {
		t.Fatalf("expected no hardcoded OC mounts in template output, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "${HOME}/.opencode:/home/agent/.opencode:ro") {
		t.Fatalf("expected no hardcoded OC mounts in template output, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "${HOME}/.claude/transcripts:/home/agent/.claude/transcripts") {
		t.Fatalf("expected no hardcoded OC mounts in template output, got:\n%s", rendered)
	}
	if strings.Contains(rendered, "${HOME}/.agents:/home/agent/.agents:ro") {
		t.Fatalf("expected no hardcoded OC mounts in template output, got:\n%s", rendered)
	}
}

func TestGenerateComposeMountOrderAfterNamedVolumes(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "order-test",
		Port:          4810,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/work"},
		Mounts: []string{
			"/host/custom:/home/agent/custom:rw",
		},
		UseDataVolume:  true,
		UseCacheVolume: true,
		CPU:            2.0,
		Memory:         "4g",
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	dataVolIdx := strings.Index(rendered, "opencode-data-order-test:/home/agent/.local/share/opencode")
	mountIdx := strings.Index(rendered, "/host/custom:/home/agent/custom:rw")

	if dataVolIdx < 0 {
		t.Fatalf("expected named data volume in output, got:\n%s", rendered)
	}
	if mountIdx < 0 {
		t.Fatalf("expected user mount in output, got:\n%s", rendered)
	}
	if mountIdx < dataVolIdx {
		t.Fatalf("expected user mounts to appear after named volumes so they take precedence;\nnamed volume at index %d, user mount at index %d", dataVolIdx, mountIdx)
	}
}

func TestGenerateComposeEnableDockerFalse(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "no-docker-test",
		Port:          4900,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/work"},
		CPU:           2.0,
		Memory:        "4g",
		EnableDocker:  false,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// DinD service must not be present
	assertNotContains(t, rendered, "dind:")
	assertNotContains(t, rendered, "docker:dind-rootless")

	// Docker env vars must not be present
	assertNotContains(t, rendered, "DOCKER_HOST")
	assertNotContains(t, rendered, "DOCKER_TLS_CERTDIR")
	assertNotContains(t, rendered, "DOCKER_CERT_PATH")
	assertNotContains(t, rendered, "DOCKER_TLS_VERIFY")

	// DinD volumes must not be present
	assertNotContains(t, rendered, "dind-certs")
	assertNotContains(t, rendered, "dind-data")

	// Basic service must still be present
	assertContains(t, rendered, "opencode:")
	assertContains(t, rendered, "- JAILOC=1")
}

func TestGenerateComposeEnableDockerTrue(t *testing.T) {
	t.Parallel()

	params := ComposeParams{
		WorkspaceName: "docker-test",
		Port:          4901,
		Image:         "ghcr.io/seznam/jailoc:test",
		Paths:         []string{"/tmp/work"},
		CPU:           2.0,
		Memory:        "4g",
		EnableDocker:  true,
	}

	out, err := GenerateCompose(params)
	if err != nil {
		t.Fatalf("GenerateCompose returned error: %v", err)
	}

	rendered := string(out)

	// DinD service must be present
	assertContains(t, rendered, "dind:")
	assertContains(t, rendered, "docker:dind-rootless")

	// Docker env vars must be present
	assertContains(t, rendered, "DOCKER_HOST=tcp://dind:2376")
	assertContains(t, rendered, "DOCKER_TLS_VERIFY=1")

	// DinD volumes must be present
	assertContains(t, rendered, "dind-certs-client:/certs/client:ro")
	assertContains(t, rendered, "dind-data:")
}
