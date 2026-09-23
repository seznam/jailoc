//go:build integration

package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const dindTestImage = "docker:dind-rootless"

func TestDindStorageSelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", dindTestImage).Run(); err != nil {
		out, pullErr := exec.CommandContext(ctx, "docker", "pull", dindTestImage).CombinedOutput()
		if pullErr != nil {
			t.Skipf("requires docker:dind-rootless image: %v\n%s", pullErr, out)
		}
	}

	failedProbe := filepath.Join(t.TempDir(), "unshare")
	if err := os.WriteFile(failedProbe, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("create failing overlay probe: %v", err)
	}
	entrypoint := filepath.Join(projectRoot(), "internal", "embed", "assets", "dind-entrypoint.sh")

	for _, tc := range []struct {
		name       string
		probeFails bool
		backend    string
		driver     string
	}{
		{name: "native", backend: "native-snapshotter", driver: "overlayfs"},
		{name: "fuse", probeFails: true, backend: "fuse-overlayfs", driver: "fuse-overlayfs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caseCtx, caseCancel := context.WithTimeout(ctx, time.Minute)
			defer caseCancel()
			name := uniqueWorkspaceName(t, "storage-"+tc.name)
			data, meta, container := name+"-data", name+"-meta", name+"-dind"
			for _, volume := range []string{data, meta} {
				dockerStorageCommand(t, caseCtx, "volume", "create", volume)
			}
			t.Cleanup(func() {
				cleanupCtx, stop := context.WithTimeout(context.Background(), time.Minute)
				defer stop()
				_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", container).Run()
				_ = exec.CommandContext(cleanupCtx, "docker", "volume", "rm", data, meta).Run()
			})

			start := func(fail bool) {
				t.Helper()
				args := []string{"run", "-d", "--pull=never", "--name", container, "--privileged", "--user", "0:0",
					"--mount", "type=volume,src=" + data + ",dst=/home/rootless/.local/share/docker",
					"--mount", "type=volume,src=" + meta + ",dst=/var/lib/jailoc/storage",
					"--mount", "type=bind,src=" + entrypoint + ",dst=/usr/local/bin/dind-entrypoint.sh,readonly"}
				if fail {
					args = append(args, "--mount", "type=bind,src="+failedProbe+",dst=/usr/local/bin/unshare,readonly")
				}
				args = append(args, "--entrypoint", "/bin/sh", dindTestImage, "/usr/local/bin/dind-entrypoint.sh")
				dockerStorageCommand(t, caseCtx, args...)
			}

			start(tc.probeFails)
			gotDriver := waitForDindDriver(t, caseCtx, container, tc.driver)
			if tc.backend == "native-snapshotter" && gotDriver == "fuse-overlayfs" {
				marker := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "exec", container, "cat", "/var/lib/jailoc/storage/storage-mode"))
				if marker != "fuse-overlayfs" {
					t.Fatalf("native overlay unavailable but marker = %q, want fuse-overlayfs", marker)
				}
				t.Skip("host kernel does not support native rootless overlayfs")
			}
			if tc.backend == "native-snapshotter" {
				status := dockerStorageCommand(t, caseCtx, "exec", container, "docker", "--host", "unix:///run/user/1000/docker.sock", "info", "--format", "{{json .DriverStatus}}")
				if !strings.Contains(status, "io.containerd.snapshotter.v1") {
					t.Fatalf("native mode did not use the containerd image store: %s", status)
				}
			}
			marker := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "exec", container, "cat", "/var/lib/jailoc/storage/storage-mode"))
			if marker != tc.backend {
				t.Fatalf("storage marker = %q, want %q", marker, tc.backend)
			}
			dockerStorageCommand(t, caseCtx, "rm", "-f", container)
			start(false)
			waitForDindDriver(t, caseCtx, container, tc.driver)
			if marker := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "exec", container, "cat", "/var/lib/jailoc/storage/storage-mode")); marker != tc.backend {
				t.Fatalf("persisted storage marker = %q, want %q", marker, tc.backend)
			}
			if tc.backend == "native-snapshotter" {
				dockerStorageCommand(t, caseCtx, "rm", "-f", container)
				start(true)
				if code := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "wait", container)); code == "0" {
					t.Fatal("pinned native backend started with an unsupported overlay probe")
				}
				if marker := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "run", "--rm", "--pull=never", "--user", "0:0", "--mount", "type=volume,src="+meta+",dst=/meta,readonly", "--entrypoint", "cat", dindTestImage, "/meta/storage-mode")); marker != tc.backend {
					t.Fatalf("pinned backend changed after failed probe: %q", marker)
				}
			} else {
				dockerStorageCommand(t, caseCtx, "exec", container, "sh", "-c", "printf '\n' >> /home/rootless/.config/docker/daemon.json")
				dockerStorageCommand(t, caseCtx, "restart", container)
				if code := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "wait", container)); code == "0" {
					t.Fatal("modified managed config unexpectedly accepted")
				}
				logs := dockerStorageCommand(t, caseCtx, "logs", container)
				if !strings.Contains(logs, "not managed by jailoc or was modified") {
					t.Fatalf("missing config ownership diagnostic: %s", logs)
				}
			}
		})
	}

	t.Run("unmarked data", func(t *testing.T) {
		caseCtx, caseCancel := context.WithTimeout(ctx, 35*time.Second)
		defer caseCancel()
		name := uniqueWorkspaceName(t, "storage-unmarked")
		data, meta, container := name+"-data", name+"-meta", name+"-dind"
		for _, volume := range []string{data, meta} {
			dockerStorageCommand(t, caseCtx, "volume", "create", volume)
		}
		t.Cleanup(func() {
			cleanupCtx, stop := context.WithTimeout(context.Background(), time.Minute)
			defer stop()
			_ = exec.CommandContext(cleanupCtx, "docker", "rm", "-f", container).Run()
			_ = exec.CommandContext(cleanupCtx, "docker", "volume", "rm", data, meta).Run()
		})
		dockerStorageCommand(t, caseCtx, "run", "--rm", "--pull=never", "--user", "0:0", "--mount", "type=volume,src="+data+",dst=/data", "--entrypoint", "mkdir", dindTestImage, "-p", "/data/overlay2")
		dockerStorageCommand(t, caseCtx, "run", "-d", "--pull=never", "--name", container, "--privileged", "--user", "0:0",
			"--mount", "type=volume,src="+data+",dst=/home/rootless/.local/share/docker",
			"--mount", "type=volume,src="+meta+",dst=/var/lib/jailoc/storage",
			"--mount", "type=bind,src="+entrypoint+",dst=/usr/local/bin/dind-entrypoint.sh,readonly",
			"--entrypoint", "/bin/sh", dindTestImage, "/usr/local/bin/dind-entrypoint.sh")
		if code := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "wait", container)); code == "0" {
			t.Fatal("unmarked populated data unexpectedly started")
		}
		logs := dockerStorageCommand(t, caseCtx, "logs", container)
		if !strings.Contains(logs, "unmarked substantive Docker data") {
			t.Fatalf("missing fail-closed diagnostic in logs: %s", logs)
		}
		if got := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "run", "--rm", "--pull=never", "--user", "0:0", "--mount", "type=volume,src="+meta+",dst=/meta", "--entrypoint", "sh", dindTestImage, "-c", "test ! -e /meta/storage-mode && printf unmarked")); got != "unmarked" {
			t.Fatalf("unmarked volume gained a backend: %q", got)
		}
		if got := strings.TrimSpace(dockerStorageCommand(t, caseCtx, "run", "--rm", "--pull=never", "--user", "0:0", "--mount", "type=volume,src="+data+",dst=/data,readonly", "--entrypoint", "sh", dindTestImage, "-c", "test -d /data/overlay2 && printf preserved")); got != "preserved" {
			t.Fatalf("preexisting Docker data was not preserved: %q", got)
		}
	})
}

func dockerStorageCommand(t *testing.T, ctx context.Context, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func waitForDindDriver(t *testing.T, ctx context.Context, container, want string) string {
	t.Helper()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		out, err := exec.CommandContext(ctx, "docker", "exec", container, "docker", "--host", "unix:///run/user/1000/docker.sock", "info", "--format", "{{.Driver}}").CombinedOutput()
		if err == nil {
			driver := strings.TrimSpace(string(out))
			if driver == want || (want == "overlayfs" && driver == "fuse-overlayfs") {
				return driver
			}
		}
		state, stateErr := exec.CommandContext(ctx, "docker", "inspect", container, "--format", "{{.State.Status}}").CombinedOutput()
		if stateErr == nil && strings.TrimSpace(string(state)) == "exited" {
			logs, _ := exec.CommandContext(ctx, "docker", "logs", container).CombinedOutput()
			t.Fatalf("%s exited before driver %s became ready: %s", container, want, logs)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("waiting for %s driver %s: %v; last output: %s", container, want, ctx.Err(), out)
		case <-ticker.C:
		}
	}
}
