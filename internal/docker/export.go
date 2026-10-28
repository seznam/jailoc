package docker

import (
	"context"
	"fmt"
	"io"
	"runtime"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
)

// SaveImage writes the image as a `docker save` tarball to w and returns its
// image ID. It talks to the daemon selected by the environment (DOCKER_HOST).
func SaveImage(ctx context.Context, ref string, w io.Writer) (string, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	inspect, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	rc, err := cli.ImageSave(ctx, []string{ref})
	if err != nil {
		return "", fmt.Errorf("save image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	if _, err := io.Copy(w, rc); err != nil {
		return "", fmt.Errorf("save image %s: %w", ref, err)
	}
	return inspect.ID, nil
}

// ImageID returns the ID of an image known to the daemon selected by the
// environment.
func ImageID(ctx context.Context, ref string) (string, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return "", fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	inspect, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("inspect image %s: %w", ref, err)
	}
	return inspect.ID, nil
}

// HostArchitecture checks that the Docker daemon selected by the environment
// answers and builds Linux images for the CPU architecture jailoc runs on,
// which is the architecture of its VMs.
func HostArchitecture(ctx context.Context) error {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	info, err := cli.Info(ctx)
	if err != nil {
		return fmt.Errorf("query Docker daemon: %w", err)
	}
	if info.OSType != "linux" {
		return fmt.Errorf("docker daemon runs %s containers, need linux", info.OSType)
	}
	if want := linuxArch(runtime.GOARCH); info.Architecture != want {
		return fmt.Errorf("docker daemon architecture is %s, VMs need %s", info.Architecture, want)
	}
	return nil
}

// linuxArch maps a Go architecture to the uname -m name the daemon reports.
func linuxArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	default:
		return goarch
	}
}

// EnsureImage makes sure the daemon has ref for linux/<host architecture>,
// the platform of the VMs. It pulls ref when the daemon lacks it or has it
// only for another platform, and rejects an image it cannot get for the
// host platform.
func EnsureImage(ctx context.Context, ref string) error {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	want := "linux/" + runtime.GOARCH
	cached := ""
	if info, err := cli.ImageInspect(ctx, ref); err == nil {
		if cached = imagePlatform(info); cached == want {
			return nil
		}
	} else if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if err := pullImage(ctx, cli, ref, want); err != nil {
		if cached != "" {
			return fmt.Errorf("image %s is for %s, VMs need %s, and pulling it for %s failed (build or pull it for %s): %w", ref, cached, want, want, want, err)
		}
		return err
	}
	info, err := cli.ImageInspect(ctx, ref)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", ref, err)
	}
	if got := imagePlatform(info); got != want {
		return fmt.Errorf("image %s is for %s, VMs need %s; use an image published for %s", ref, got, want, want)
	}
	return nil
}

func pullImage(ctx context.Context, cli *dockerclient.Client, ref, platform string) error {
	rc, err := cli.ImagePull(ctx, ref, image.PullOptions{Platform: platform})
	if err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	if err := displayStream(rc); err != nil {
		return fmt.Errorf("pull image %s: %w", ref, err)
	}
	return nil
}

// imagePlatform returns the os/architecture of an inspected image.
func imagePlatform(info image.InspectResponse) string {
	return info.Os + "/" + info.Architecture
}
