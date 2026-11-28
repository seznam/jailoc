package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"time"

	goversion "github.com/hashicorp/go-version"
)

const (
	// releaseCooldown is the minimum age a release must reach before it is offered.
	releaseCooldown = 24 * time.Hour
	// releasePageSize and maxReleasePages bound a listing to 50 releases. The listing is
	// read exhaustively because backports make it not semver sorted.
	releasePageSize  = 10
	maxReleasePages  = 5
	releasePageLimit = int64(4 << 20)
	githubAPIVersion = "2022-11-28"
)

var tagPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

type githubAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubRelease struct {
	TagName     string        `json:"tag_name"`
	PublishedAt time.Time     `json:"published_at"`
	Draft       bool          `json:"draft"`
	Prerelease  bool          `json:"prerelease"`
	Immutable   bool          `json:"immutable"`
	Assets      []githubAsset `json:"assets"`
}

// releaseCandidate is the minimal release data needed to decide what to offer; it is cached on disk.
type releaseCandidate struct {
	Tag         string    `json:"tag"`
	PublishedAt time.Time `json:"published_at"`
	Prerelease  bool      `json:"prerelease"`
}

// releaseWire decodes a release leniently: published_at stays raw so a malformed
// timestamp rejects only that release.
type releaseWire struct {
	TagName     string          `json:"tag_name"`
	PublishedAt json.RawMessage `json:"published_at"`
	Draft       bool            `json:"draft"`
	Prerelease  bool            `json:"prerelease"`
	Immutable   bool            `json:"immutable"`
	Assets      []githubAsset   `json:"assets"`
}

func (w releaseWire) release() (githubRelease, error) {
	if w.TagName == "" {
		return githubRelease{}, fmt.Errorf("missing tag_name")
	}

	var raw string
	if len(w.PublishedAt) > 0 {
		if err := json.Unmarshal(w.PublishedAt, &raw); err != nil {
			return githubRelease{}, fmt.Errorf("release %q: malformed published_at: %w", w.TagName, err)
		}
	}
	if raw == "" {
		return githubRelease{}, fmt.Errorf("release %q: missing published_at", w.TagName)
	}

	published, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return githubRelease{}, fmt.Errorf("release %q: malformed published_at: %w", w.TagName, err)
	}
	if published.IsZero() {
		return githubRelease{}, fmt.Errorf("release %q: zero published_at", w.TagName)
	}

	return githubRelease{
		TagName:     w.TagName,
		PublishedAt: published,
		Draft:       w.Draft,
		Prerelease:  w.Prerelease,
		Immutable:   w.Immutable,
		Assets:      w.Assets,
	}, nil
}

// normalizeTag accepts vX.Y.Z or X.Y.Z with an optional prerelease and returns the v-prefixed tag.
func normalizeTag(tag string) (string, error) {
	if !tagPattern.MatchString(tag) {
		return "", fmt.Errorf("invalid release tag %q: want vX.Y.Z", tag)
	}

	if tag[0] != 'v' {
		tag = "v" + tag
	}

	if _, err := goversion.NewVersion(tag); err != nil {
		return "", fmt.Errorf("invalid release tag %q: %w", tag, err)
	}

	return tag, nil
}

func getReleaseBody(ctx context.Context, requestURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create release request for %s: %w", requestURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "jailoc-update-checker")

	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Do(req) //nolint:gosec // requestURL derives from a caller-provided release URL that defaults to the GitHub API
	if err != nil {
		return nil, fmt.Errorf("execute release request to %s: %w", requestURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch releases from %s: unexpected status %d", requestURL, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, releasePageLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read release body from %s: %w", requestURL, err)
	}
	if int64(len(data)) > releasePageLimit {
		return nil, fmt.Errorf("release response from %s exceeds 4MiB limit", requestURL)
	}

	return data, nil
}

// fetchReleases reads up to maxReleasePages pages of the releases list endpoint.
// Releases with a missing tag or missing/malformed publication time are skipped individually.
func fetchReleases(ctx context.Context, releaseURL string) ([]githubRelease, error) {
	base, err := url.Parse(releaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse release URL %q: %w", releaseURL, err)
	}

	var releases []githubRelease

	for page := 1; page <= maxReleasePages; page++ {
		u := *base
		q := u.Query()
		q.Set("per_page", fmt.Sprint(releasePageSize))
		q.Set("page", fmt.Sprint(page))
		u.RawQuery = q.Encode()

		data, err := getReleaseBody(ctx, u.String())
		if err != nil {
			return nil, err
		}

		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("decode release response from %s: %w", u.String(), err)
		}

		for _, item := range items {
			var w releaseWire
			if err := json.Unmarshal(item, &w); err != nil {
				continue
			}

			r, err := w.release()
			if err != nil {
				continue
			}

			releases = append(releases, r)
		}

		if len(items) < releasePageSize {
			break
		}
	}

	return releases, nil
}

// fetchRelease reads one release from <releaseURL>/tags/<tag>.
func fetchRelease(ctx context.Context, releaseURL, tag string) (githubRelease, error) {
	normalized, err := normalizeTag(tag)
	if err != nil {
		return githubRelease{}, err
	}

	base, err := url.Parse(releaseURL)
	if err != nil {
		return githubRelease{}, fmt.Errorf("parse release URL %q: %w", releaseURL, err)
	}

	requestURL := base.JoinPath("tags", normalized).String()

	data, err := getReleaseBody(ctx, requestURL)
	if err != nil {
		return githubRelease{}, err
	}

	var w releaseWire
	if err := json.Unmarshal(data, &w); err != nil {
		return githubRelease{}, fmt.Errorf("decode release response from %s: %w", requestURL, err)
	}

	r, err := w.release()
	if err != nil {
		return githubRelease{}, fmt.Errorf("decode release response from %s: %w", requestURL, err)
	}

	if r.TagName != normalized {
		return githubRelease{}, fmt.Errorf("release response from %s: tag mismatch: got %q, want %q", requestURL, r.TagName, normalized)
	}

	return r, nil
}

// candidatesFromReleases drops drafts and releases without a valid tag or publication time.
func candidatesFromReleases(releases []githubRelease) []releaseCandidate {
	candidates := make([]releaseCandidate, 0, len(releases))

	for _, r := range releases {
		if r.Draft || r.PublishedAt.IsZero() {
			continue
		}
		if _, err := normalizeTag(r.TagName); err != nil {
			continue
		}

		candidates = append(candidates, releaseCandidate{Tag: r.TagName, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease})
	}

	return candidates
}

// selectRelease returns the highest stable candidate that is at least releaseCooldown old (eligible)
// and the highest stable candidate still cooling down that is newer than eligible (pending).
// noCooldown skips the age requirement; publication times in the future are never selected.
func selectRelease(candidates []releaseCandidate, now time.Time, noCooldown bool) (eligible, pending *releaseCandidate) {
	var eligibleV, pendingV *goversion.Version

	for _, c := range candidates {
		if c.Prerelease || c.PublishedAt.IsZero() || c.PublishedAt.After(now) {
			continue
		}

		tag, err := normalizeTag(c.Tag)
		if err != nil {
			continue
		}

		v, err := goversion.NewVersion(tag)
		if err != nil || v.Prerelease() != "" {
			continue
		}

		if noCooldown || now.Sub(c.PublishedAt) >= releaseCooldown {
			if eligibleV == nil || v.GreaterThan(eligibleV) {
				chosen := c
				eligible, eligibleV = &chosen, v
			}
			continue
		}

		if pendingV == nil || v.GreaterThan(pendingV) {
			chosen := c
			pending, pendingV = &chosen, v
		}
	}

	if pending != nil && eligibleV != nil && !pendingV.GreaterThan(eligibleV) {
		pending = nil
	}

	return eligible, pending
}
