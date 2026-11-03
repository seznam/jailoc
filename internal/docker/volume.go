package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	dockerclient "github.com/docker/docker/client"
)

// exportImage is the helper image whose container mounts the volume during
// an export; the container is created but never started.
const exportImage = "busybox:latest"

// VolumeName returns the Docker name of a compose volume of workspace ws.
func VolumeName(ws, key string) string {
	return "jailoc-" + ws + "_" + key
}

// Ping reports whether the Docker daemon selected by the environment answers.
func Ping(ctx context.Context) error {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping Docker daemon: %w", err)
	}
	return nil
}

func RunningComposeContainers(ctx context.Context, workspace string) ([]container.Summary, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "com.docker.compose.project=jailoc-"+workspace),
			filters.Arg("status", "running"),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("list workspace %q containers: %w", workspace, err)
	}
	return containers, nil
}

func StopComposeContainers(ctx context.Context, workspace string) error {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "com.docker.compose.project=jailoc-"+workspace)),
	})
	if err != nil {
		return fmt.Errorf("list workspace %q containers: %w", workspace, err)
	}
	for _, current := range containers {
		if current.State == "running" {
			if err := cli.ContainerStop(ctx, current.ID, container.StopOptions{}); err != nil {
				return fmt.Errorf("stop workspace %q container %s: %w", workspace, current.ID, err)
			}
		}
		if err := cli.ContainerRemove(ctx, current.ID, container.RemoveOptions{}); err != nil {
			return fmt.Errorf("remove workspace %q container %s: %w", workspace, current.ID, err)
		}
	}
	return nil
}

// ExportVolume copies the content of the named volume into dst, a path
// relative to root that must not exist yet. It reports false when the
// volume does not exist.
func ExportVolume(ctx context.Context, volume string, root *os.Root, dst string) (bool, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return false, fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	if _, err := cli.VolumeInspect(ctx, volume); err != nil {
		if cerrdefs.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("inspect volume %s: %w", volume, err)
	}
	if _, err := cli.ImageInspect(ctx, exportImage); err != nil {
		if !cerrdefs.IsNotFound(err) {
			return false, fmt.Errorf("inspect image %s: %w", exportImage, err)
		}
		rc, err := cli.ImagePull(ctx, exportImage, image.PullOptions{})
		if err != nil {
			return false, fmt.Errorf("pull %s: %w", exportImage, err)
		}
		_, err = io.Copy(io.Discard, rc)
		_ = rc.Close()
		if err != nil {
			return false, fmt.Errorf("pull %s: %w", exportImage, err)
		}
	}
	created, err := cli.ContainerCreate(ctx,
		&container.Config{Image: exportImage, Cmd: []string{"true"}},
		&container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: "/volume", ReadOnly: true}}},
		nil, nil, "")
	if err != nil {
		return false, fmt.Errorf("create export container for %s: %w", volume, err)
	}
	defer func() {
		_ = cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
	}()
	rc, _, err := cli.CopyFromContainer(ctx, created.ID, "/volume")
	if err != nil {
		return false, fmt.Errorf("read volume %s: %w", volume, err)
	}
	defer func() { _ = rc.Close() }()
	if err := extractTar(rc, root, dst); err != nil {
		return false, fmt.Errorf("extract volume %s: %w", volume, err)
	}
	return true, nil
}

// extractTar unpacks an archive whose entries share one top-level directory
// into dst under parent, dropping that directory. Entries escaping dst are rejected;
// devices, pipes and hard links are skipped. Symbolic links are kept as
// they are, since they name paths in the VM, and every write goes through
// an os.Root so that no link leads it outside dst.
func extractTar(r io.Reader, parent *os.Root, dst string) error {
	if err := parent.MkdirAll(dst, 0o700); err != nil {
		return fmt.Errorf("create destination: %w", err)
	}
	root, err := parent.OpenRoot(dst)
	if err != nil {
		return fmt.Errorf("open destination: %w", err)
	}
	defer func() { _ = root.Close() }()

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("archive entry %q is outside the destination", hdr.Name)
		}
		_, rel, found := strings.Cut(name, "/")
		if !found || rel == "" {
			continue
		}
		mode := os.FileMode(hdr.Mode).Perm() //nolint:gosec // G115: tar mode bits fit in FileMode
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(rel, mode|0o700); err != nil {
				return fmt.Errorf("create archive directory %q: %w", hdr.Name, err)
			}
		case tar.TypeReg:
			if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
				return fmt.Errorf("create parent for archive file %q: %w", hdr.Name, err)
			}
			f, err := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o600)
			if err != nil {
				return fmt.Errorf("create archive file %q: %w", hdr.Name, err)
			}
			if _, err := io.Copy(f, tr); err != nil { //nolint:gosec // G110: volume content is the user's own data
				_ = f.Close()
				return fmt.Errorf("write archive file %q: %w", hdr.Name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("close archive file %q: %w", hdr.Name, err)
			}
		case tar.TypeSymlink:
			if err := root.MkdirAll(filepath.Dir(rel), 0o700); err != nil {
				return fmt.Errorf("create parent for archive link %q: %w", hdr.Name, err)
			}
			if err := root.Symlink(hdr.Linkname, rel); err != nil {
				return fmt.Errorf("create archive link %q: %w", hdr.Name, err)
			}
		}
	}
}
