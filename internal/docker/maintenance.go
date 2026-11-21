package docker

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/moby/moby/api/types/image"
	dockerclient "github.com/moby/moby/client"
)

// builderImageGrace spares recently built images: they may belong to a build
// in flight or one that failed before it reached the VM image cache.
const builderImageGrace = time.Hour

// Maintenance reports what one builder maintenance run reclaimed.
type Maintenance struct {
	// CacheReclaimed is the build cache space the daemon freed, in bytes.
	CacheReclaimed uint64
	CachesDeleted  int
	ImagesRemoved  int
}

// MaintainBuilder trims the build cache and removes obsolete jailoc images of
// the Docker daemon selected by the environment (DOCKER_HOST). Only call it
// for a daemon jailoc owns, such as the one of a builder VM, never for a host
// daemon other tools and HOMEs share.
//
// diskSize is the size of the daemon's data disk. The build cache is pruned
// to at most half of it, and to leave a fifth of it free, on a best-effort
// basis: the daemon still keeps what a running build uses, and the limits
// are no quota. keep holds the image IDs (with or without the sha256: prefix)
// the VM image cache still holds; images that jailoc built, that are not in
// keep, older than an hour, that carry no foreign tag and that no other kept
// or young image builds on are removed, never forcibly. Failures of the two
// steps are joined; a failed step does not skip the other.
func MaintainBuilder(ctx context.Context, diskSize int64, keep map[string]bool) (Maintenance, error) {
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return Maintenance{}, fmt.Errorf("create Docker Engine client: %w", err)
	}
	defer func() { _ = cli.Close() }()
	return maintainBuilder(ctx, cli, diskSize, keep, time.Now())
}

func maintainBuilder(ctx context.Context, cli dockerclient.APIClient, diskSize int64, keep map[string]bool, now time.Time) (Maintenance, error) {
	var res Maintenance
	if diskSize <= 0 {
		return res, fmt.Errorf("builder disk size %d must be positive", diskSize)
	}
	var errs []error

	pruned, err := cli.BuildCachePrune(ctx, dockerclient.BuildCachePruneOptions{
		All:          true,
		MaxUsedSpace: diskSize / 2,
		MinFreeSpace: diskSize / 5,
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("prune build cache: %w", err))
	} else {
		res.CacheReclaimed = pruned.Report.SpaceReclaimed
		res.CachesDeleted = len(pruned.Report.CachesDeleted)
	}

	res.ImagesRemoved, err = removeObsoleteImages(ctx, cli, keep, now.Add(-builderImageGrace))
	if err != nil {
		errs = append(errs, err)
	}
	return res, errors.Join(errs...)
}

// removeObsoleteImages removes the labeled images that nothing protects. An
// image is protected when it is in keep, was created after cutoff, is used by
// a container (running or stopped), carries a tag outside the jailoc-*
// repositories (a user's image built on a jailoc one inherits its label), or
// is an ancestor of a protected image: the base and agent layers a kept image
// was built from. Removal names one image at a time and never prunes children,
// so untagged parents are only removed when selected themselves. When the
// containers cannot be listed no image is removed.
func removeObsoleteImages(ctx context.Context, cli dockerclient.APIClient, keep map[string]bool, cutoff time.Time) (int, error) {
	list, err := cli.ImageList(ctx, dockerclient.ImageListOptions{
		All:     true,
		Filters: make(dockerclient.Filters).Add("label", ImageLabel),
	})
	if err != nil {
		return 0, fmt.Errorf("list jailoc images: %w", err)
	}
	// The image list's per-image container count is unavailable (-1) unless
	// asked for, so the containers themselves say which images are in use.
	containers, err := cli.ContainerList(ctx, dockerclient.ContainerListOptions{All: true})
	if err != nil {
		return 0, fmt.Errorf("list containers: %w", err)
	}
	used := make(map[string]bool, len(containers.Items))
	for _, c := range containers.Items {
		if c.ImageID != "" {
			used[strings.TrimPrefix(c.ImageID, "sha256:")] = true
		}
	}

	var protected, candidates []image.Summary
	for _, img := range list.Items {
		if img.Labels[ImageLabel] == "" {
			continue
		}
		hex := strings.TrimPrefix(img.ID, "sha256:")
		if keep[hex] || keep["sha256:"+hex] || used[hex] || time.Unix(img.Created, 0).After(cutoff) || img.Containers > 0 || hasForeignTag(img) {
			protected = append(protected, img)
		} else {
			candidates = append(candidates, img)
		}
	}
	if len(candidates) == 0 {
		return 0, nil
	}

	// Every image a container uses protects its ancestors, listed or not.
	protectedIDs := make([]string, 0, len(protected)+len(used))
	for _, img := range protected {
		protectedIDs = append(protectedIDs, img.ID)
	}
	for hex := range used {
		protectedIDs = append(protectedIDs, "sha256:"+hex)
	}
	var protectedLayers [][]string
	for _, id := range protectedIDs {
		layers, err := imageLayers(ctx, cli, id)
		if err != nil {
			return 0, fmt.Errorf("inspect protected image %s: %w", id, err)
		}
		protectedLayers = append(protectedLayers, layers)
	}

	removed := 0
	var errs []error
candidates:
	for _, img := range candidates {
		layers, err := imageLayers(ctx, cli, img.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("inspect image %s: %w", img.ID, err))
			continue
		}
		for _, p := range protectedLayers {
			if len(layers) <= len(p) && slices.Equal(layers, p[:len(layers)]) {
				continue candidates
			}
		}
		failed := false
		for _, ref := range removalRefs(img) {
			// Neither forced nor pruning children: the daemon keeps what a
			// container or another image uses and never removes an image
			// that was not selected here.
			if _, err := cli.ImageRemove(ctx, ref, dockerclient.ImageRemoveOptions{}); err != nil {
				errs = append(errs, fmt.Errorf("remove image %s: %w", ref, err))
				failed = true
			}
		}
		if !failed {
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// imageLayers returns the layer digests of the image id, the bottom one first.
func imageLayers(ctx context.Context, cli dockerclient.APIClient, id string) ([]string, error) {
	info, err := cli.ImageInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("read image layers: %w", err)
	}
	return info.RootFS.Layers, nil
}

// hasForeignTag reports whether img has a tag outside the jailoc-* repositories.
func hasForeignTag(img image.Summary) bool {
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" && !strings.HasPrefix(tag, "jailoc-") {
			return true
		}
	}
	return false
}

// removalRefs returns the references to remove for img: its tags, or its ID
// when it has none.
func removalRefs(img image.Summary) []string {
	var refs []string
	for _, tag := range img.RepoTags {
		if tag != "<none>:<none>" {
			refs = append(refs, tag)
		}
	}
	if len(refs) == 0 {
		refs = append(refs, img.ID)
	}
	return refs
}
