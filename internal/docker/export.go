package docker

import (
	"context"
	"fmt"
	"io"

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
