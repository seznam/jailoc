package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/seznam/jailoc/internal/agent"
)

// githubAPI is the GitHub REST API that publishes the checksums of release
// assets.
const githubAPI = "https://api.github.com"

// sha256Re matches a hex SHA-256 checksum; it ends up in a shell command
// of the image layer.
var sha256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// resolveRelease returns r for version: r itself when its checksums are for
// version, otherwise r with the checksums GitHub publishes for the assets
// of the release tagged version.
func resolveRelease(ctx context.Context, apiBase string, r agent.Release, version string) (agent.Release, error) {
	if version == r.Version {
		return r, nil
	}
	sums, err := releaseChecksums(ctx, apiBase, r, version)
	if err != nil {
		return agent.Release{}, err
	}
	r.Version = version
	r.SHA256 = sums
	return r, nil
}

// releaseChecksums returns the SHA-256 checksums of the assets of r's
// release tagged version by architecture, from the asset digests of the
// GitHub API.
func releaseChecksums(ctx context.Context, apiBase string, r agent.Release, version string) (map[string]string, error) {
	rawURL := fmt.Sprintf("%s/repos/%s/releases/tags/%s", apiBase, r.Repo, url.PathEscape(version))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request for %s: %w", rawURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req) //nolint:gosec // the URL is built from a built-in repository and a validated version
	if err != nil {
		return nil, fmt.Errorf("fetch release %s of %s: %w", version, r.Repo, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch release %s of %s: unexpected status %d", version, r.Repo, resp.StatusCode)
	}
	var release struct {
		Assets []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&release); err != nil {
		return nil, fmt.Errorf("decode release %s of %s: %w", version, r.Repo, err)
	}
	sums := make(map[string]string, len(agent.ReleaseArchs))
	for _, arch := range agent.ReleaseArchs {
		name := r.AssetName(arch)
		for _, a := range release.Assets {
			if a.Name != name {
				continue
			}
			sum, ok := strings.CutPrefix(a.Digest, "sha256:")
			if !ok || !sha256Re.MatchString(sum) {
				return nil, fmt.Errorf("release %s of %s: asset %s has no SHA-256 digest", version, r.Repo, name)
			}
			sums[arch] = sum
		}
		if sums[arch] == "" {
			return nil, fmt.Errorf("release %s of %s has no asset %s", version, r.Repo, name)
		}
	}
	return sums, nil
}
