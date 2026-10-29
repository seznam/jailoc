package docker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	dockerclient "github.com/docker/docker/client"
)

// ImageLabel marks the images jailoc builds, so PruneImages also finds them
// once a rebuild has left them untagged.
const ImageLabel = "com.github.seznam.jailoc"

// buildLabels are the labels of every image jailoc builds.
func buildLabels() map[string]string { return map[string]string{ImageLabel: "1"} }

// PruneImages removes the images jailoc built that are not in keep (image
// IDs) and older than grace, together with their untagged intermediate
// images, from the daemon selected by the environment. An image is jailoc's
// when it carries ImageLabel or a jailoc-* tag, and only when all its tags
// are jailoc-* ones. It returns the number of images removed; removal
// failures, e.g. of an image a container uses, are returned joined.
func PruneImages(ctx context.Context, keep map[string]bool, grace time.Duration) (int, error) {
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return 0, fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	var images []image.Summary
	for _, f := range []filters.KeyValuePair{filters.Arg("label", ImageLabel), filters.Arg("reference", "jailoc-*")} {
		list, err := cli.ImageList(ctx, image.ListOptions{Filters: filters.NewArgs(f)})
		if err != nil {
			return 0, fmt.Errorf("list jailoc images: %w", err)
		}
		images = append(images, list...)
	}

	removed := 0
	var errs []string
	for _, ref := range pruneCandidates(images, keep, time.Now().Add(-grace)) {
		if _, err := cli.ImageRemove(ctx, ref, image.RemoveOptions{PruneChildren: true}); err != nil {
			errs = append(errs, fmt.Sprintf("remove image %s: %v", ref, err))
			continue
		}
		removed++
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return removed, nil
}

// pruneCandidates returns the references to remove from images: the tags of
// a tagged image, the ID of an untagged one. It skips images in keep, images
// created after cutoff and images with a tag outside the jailoc-* repos.
func pruneCandidates(images []image.Summary, keep map[string]bool, cutoff time.Time) []string {
	seen := map[string]bool{}
	var refs []string
	for _, img := range images {
		if seen[img.ID] || keep[img.ID] || time.Unix(img.Created, 0).After(cutoff) {
			continue
		}
		seen[img.ID] = true
		var tags []string
		foreign := false
		for _, tag := range img.RepoTags {
			if tag == "<none>:<none>" {
				continue
			}
			if !strings.HasPrefix(tag, "jailoc-") {
				foreign = true
				break
			}
			tags = append(tags, tag)
		}
		switch {
		case foreign:
		case len(tags) == 0:
			refs = append(refs, img.ID)
		default:
			refs = append(refs, tags...)
		}
	}
	return refs
}
