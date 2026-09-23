//go:build integration

package integration_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDindLegacyOverlay2Migration(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	if !dockerAvailable(ctx) {
		t.Skip("requires Docker daemon")
	}
	if err := exec.CommandContext(ctx, "docker", "image", "inspect", dindTestImage).Run(); err != nil {
		dockerStorageCommand(t, ctx, "pull", dindTestImage)
	}

	name := uniqueWorkspaceName(t, "legacy-overlay2")
	data, meta, container, probe := name+"-data", name+"-meta", name+"-dind", name+"-probe"
	t.Cleanup(func() {
		cleanupCtx, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		// Some resources may not exist after a skip or a partially completed setup.
		for _, resource := range []string{container, probe} {
			cmd := exec.CommandContext(cleanupCtx, "docker")
			cmd.Args = append(cmd.Args, "rm", "-fv", resource)
			if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "No such container") {
				t.Errorf("remove %s: %v\n%s", resource, err, out)
			}
		}
		for _, volume := range []string{data, meta} {
			cmd := exec.CommandContext(cleanupCtx, "docker")
			cmd.Args = append(cmd.Args, "volume", "rm", volume)
			if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "no such volume") {
				t.Errorf("remove %s: %v\n%s", volume, err, out)
			}
		}
	})
	for _, volume := range []string{data, meta} {
		dockerStorageCommand(t, ctx, "volume", "create", volume)
	}
	dockerStorageCommand(t, ctx, "run", "--rm", "--pull=never", "--user", "0:0",
		"--mount", "type=volume,src="+data+",dst=/data", "--entrypoint", "chown", dindTestImage, "1000:1000", "/data")

	// Probe the kernel on the actual backing filesystem, independently of jailoc.
	const overlayProbe = `set -eu
test "$(id -u)" = 1000
command -v unshare
command -v mount
d=$(mktemp -d /data/probe.XXXXXX)
mkdir "$d/lower" "$d/upper" "$d/work" "$d/merged"
result=77
for option in userxattr, ""; do
  if unshare -Ur -m sh -c 'mount -t overlay overlay -o "${2}lowerdir=$1/lower,upperdir=$1/upper,workdir=$1/work" "$1/merged" || exit 77; umount "$1/merged" || exit 2' sh "$d" "$option"; then
    result=0
    break
  else
    status=$?
    case "$status" in 1|77) ;; *) exit "$status" ;; esac
  fi
  if [ -d "$d/work/work" ]; then rmdir "$d/work/work"; fi
done
if [ -d "$d/work/work" ]; then rmdir "$d/work/work"; fi
rmdir "$d/merged" "$d/work" "$d/upper" "$d/lower" "$d"
exit "$result"`
	probeCmd := exec.CommandContext(ctx, "docker")
	probeCmd.Args = append(probeCmd.Args, "run", "--rm", "--pull=never", "--name", probe,
		"--privileged", "--user", "1000:1000", "--mount", "type=volume,src="+data+",dst=/data",
		"--entrypoint", "sh", dindTestImage, "-c", overlayProbe)
	out, err := probeCmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 77 {
			t.Skipf("independent rootless overlay mount probe unsupported: %s", out)
		}
		t.Fatalf("rootless overlay capability probe: %v\n%s", err, out)
	}

	// Populate the old classic store before creating any jailoc metadata.
	dockerStorageCommand(t, ctx, "run", "-d", "--pull=never", "--name", container,
		"--privileged", "--user", "1000:1000", "-e", "DOCKER_TLS_CERTDIR=",
		"--mount", "type=volume,src="+data+",dst=/home/rootless/.local/share/docker",
		dindTestImage, "--storage-driver=overlay2", "--feature=containerd-snapshotter=false")
	waitForDindDriver(t, ctx, container, "overlay2")
	inner := func(args ...string) string {
		t.Helper()
		command := append([]string{"exec", container, "docker", "--host", "unix:///run/user/1000/docker.sock"}, args...)
		return strings.TrimSpace(dockerStorageCommand(t, ctx, command...))
	}
	dockerStorageCommand(t, ctx, "exec", container, "sh", "-ec",
		"tar -C / -cf /tmp/legacy.tar bin/busybox; docker --host unix:///run/user/1000/docker.sock import /tmp/legacy.tar jailoc-legacy-fixture:local")
	imageID := inner("image", "inspect", "--format", "{{.Id}}", "jailoc-legacy-fixture:local")
	containerID := inner("create", "--name", "legacy-fixture", "jailoc-legacy-fixture:local", "/bin/busybox", "true")
	assertStore := func() {
		t.Helper()
		waitForDindDriver(t, ctx, container, "overlay2")
		if status := inner("info", "--format", "{{json .DriverStatus}}"); strings.Contains(status, "io.containerd.snapshotter.v1") {
			t.Fatalf("classic store unexpectedly uses containerd snapshotter: %s", status)
		}
		if got := inner("image", "inspect", "--format", "{{.Id}}", "jailoc-legacy-fixture:local"); got != imageID {
			t.Fatalf("legacy image ID = %q, want %q", got, imageID)
		}
		if got := inner("container", "inspect", "--format", "{{.Id}}", "legacy-fixture"); got != containerID {
			t.Fatalf("legacy container ID = %q, want %q", got, containerID)
		}
	}
	assertStore()
	dockerStorageCommand(t, ctx, "stop", container)
	dockerStorageCommand(t, ctx, "rm", "-v", container)

	// Seed only the verified mode; the helper never mounts or changes the data.
	dockerStorageCommand(t, ctx, "run", "--rm", "--pull=never", "--user", "0:0",
		"--mount", "type=volume,src="+meta+",dst=/meta", "--entrypoint", "sh", dindTestImage, "-ec",
		"test ! -e /meta/storage-mode; chmod 700 /meta; umask 077; printf 'legacy-overlay2\\n' > /meta/storage-mode")
	entrypoint := filepath.Join(projectRoot(), "internal", "embed", "assets", "dind-entrypoint.sh")
	start := func() {
		t.Helper()
		dockerStorageCommand(t, ctx, "run", "-d", "--pull=never", "--name", container,
			"--privileged", "--user", "0:0", "-e", "DOCKER_TLS_CERTDIR=",
			"--mount", "type=volume,src="+data+",dst=/home/rootless/.local/share/docker",
			"--mount", "type=volume,src="+meta+",dst=/var/lib/jailoc/storage",
			"--mount", "type=bind,src="+entrypoint+",dst=/usr/local/bin/dind-entrypoint.sh,readonly",
			"--entrypoint", "/bin/sh", dindTestImage, "/usr/local/bin/dind-entrypoint.sh")
	}
	start()
	assertStore()
	dockerStorageCommand(t, ctx, "restart", container)
	assertStore()
	dockerStorageCommand(t, ctx, "stop", container)
	dockerStorageCommand(t, ctx, "rm", "-v", container)
	start()
	assertStore()
}
