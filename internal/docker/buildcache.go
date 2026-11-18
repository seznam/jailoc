package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime"
	"sort"

	dockerclient "github.com/moby/moby/client"
)

// ImageLabel marks the images jailoc builds.
const ImageLabel = "com.github.seznam.jailoc"

// buildLabels are the labels of every image jailoc builds.
func buildLabels() map[string]string { return map[string]string{ImageLabel: "1"} }

// ContentLabel records the hash of the Dockerfile and build arguments an
// image was built from, so an unchanged build can be skipped.
const ContentLabel = "com.github.seznam.jailoc.content"

// contentHash returns the ContentLabel value of a build of dockerfile with args.
func contentHash(dockerfile []byte, args map[string]*string) string {
	h := sha256.New()
	h.Write(dockerfile)
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if v := args[k]; v != nil {
			_, _ = fmt.Fprintf(h, "\x00%s=%s", k, *v)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// haveImage reports whether the daemon has tag for the VM platform and,
// when hash is set, built from content with that ContentLabel.
func haveImage(ctx context.Context, cli dockerclient.APIClient, tag, hash string) bool {
	info, err := cli.ImageInspect(ctx, tag)
	if err != nil || imagePlatform(info) != "linux/"+runtime.GOARCH {
		return false
	}
	return hash == "" || info.Config != nil && info.Config.Labels[ContentLabel] == hash
}
