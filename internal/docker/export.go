package docker

import (
	"context"
	"fmt"
	"io"
	"runtime"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/containerd/platforms"
	dockerclient "github.com/moby/moby/client"
	"github.com/moby/moby/client/pkg/versions"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// SaveImage writes the image as a `docker save` tarball to w and returns its
// image ID. It talks to the daemon selected by the environment (DOCKER_HOST).
func SaveImage(ctx context.Context, ref string, w io.Writer) (string, error) {
	cli, err := dockerclient.New(dockerclient.FromEnv)
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
	cli, err := dockerclient.New(dockerclient.FromEnv)
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

// minSaveAPIVersion is the Engine API version of Docker Engine 25, the first
// release whose `docker save` writes an OCI image layout.
const minSaveAPIVersion = "1.44"

// HostArchitecture checks that the Docker daemon selected by the environment
// answers, is Docker Engine 25 or later, and builds Linux images for the CPU
// architecture jailoc runs on, which is the architecture of its VMs.
func HostArchitecture(ctx context.Context) error {
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	info, err := cli.Info(ctx, dockerclient.InfoOptions{})
	if err != nil {
		return fmt.Errorf("query Docker daemon: %w", err)
	}
	if info.Info.OSType != "linux" {
		return fmt.Errorf("docker daemon runs %s containers, need linux", info.Info.OSType)
	}
	if want := linuxArch(runtime.GOARCH); info.Info.Architecture != want {
		return fmt.Errorf("docker daemon architecture is %s, VMs need %s", info.Info.Architecture, want)
	}
	v, err := cli.ServerVersion(ctx, dockerclient.ServerVersionOptions{})
	if err != nil {
		return fmt.Errorf("query Docker daemon version: %w", err)
	}
	return checkSaveAPIVersion(v.Version, v.APIVersion)
}

// checkSaveAPIVersion rejects a daemon whose `docker save` predates the OCI
// image layout jailoc imports.
func checkSaveAPIVersion(version, apiVersion string) error {
	if versions.LessThan(apiVersion, minSaveAPIVersion) {
		return fmt.Errorf("docker daemon %s (API %s) is older than Docker Engine 25 (API %s)", version, apiVersion, minSaveAPIVersion)
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
	cli, err := dockerclient.New(dockerclient.FromEnv)
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
	p, err := platforms.Parse(platform)
	if err != nil {
		return fmt.Errorf("parse platform %s: %w", platform, err)
	}
	rc, err := cli.ImagePull(ctx, ref, dockerclient.ImagePullOptions{Platforms: []ocispec.Platform{p}})
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
func imagePlatform(info dockerclient.ImageInspectResult) string {
	return info.Os + "/" + info.Architecture
}
