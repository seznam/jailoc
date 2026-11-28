package update

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	goversion "github.com/hashicorp/go-version"
)

const (
	// DefaultReleaseURL is the GitHub releases list endpoint.
	DefaultReleaseURL = "https://api.github.com/repos/seznam/jailoc/releases"
	cacheTTL          = 24 * time.Hour
	httpTimeout       = 10 * time.Second
	releaseTagPageURL = "https://github.com/seznam/jailoc/releases/tag/"
)

type state struct {
	CheckedAt time.Time `json:"checked_at"`
	// LatestVersion is the eligible release at fetch time; kept for older readers.
	LatestVersion string `json:"latest_version"`
	// Candidates are the fetched releases; the 24h cooldown is re-evaluated from them on every use.
	Candidates []releaseCandidate `json:"candidates,omitempty"`
}

// Result describes an available update. Latest is the highest stable release
// that passed the cooldown; Pending is a newer stable release still cooling down, if any.
type Result struct {
	Current string
	Latest  string
	Pending string
}

func shouldCheck(version string) bool {
	if version == "dev" || version == "(devel)" {
		return false
	}

	if os.Getenv("CI") != "" {
		return false
	}

	if os.Getenv("JAILOC_NO_UPDATE_NOTIFIER") != "" {
		return false
	}

	return true
}

func loadState(path string) (state, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is internal cache file path provided by caller
	if err != nil {
		return state{}, fmt.Errorf("read state file %q: %w", path, err)
	}

	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return state{}, fmt.Errorf("decode state file %q: %w", path, err)
	}

	return s, nil
}

func saveState(path string, s state) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // cache directory is intentionally user-readable/executable
		return fmt.Errorf("create state directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, "update-state-*.json")
	if err != nil {
		return fmt.Errorf("create temp state file in %q: %w", dir, err)
	}
	tmpPath := tmp.Name()

	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()

	enc := json.NewEncoder(tmp)
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("encode state file %q: %w", tmpPath, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp state file %q: %w", tmpPath, err)
	}

	if err := os.Rename(tmpPath, path); err != nil { //nolint:gosec // path is internal cache file path from defaultStatePath()
		return fmt.Errorf("replace state file %q: %w", path, err)
	}

	return nil
}

func defaultStatePath() string {
	base, err := os.UserHomeDir()
	if err != nil {
		base = os.TempDir()
	}

	return filepath.Join(base, ".cache", "jailoc", "update-state.json")
}

// releaseCandidates returns cached release candidates while the cache is younger than cacheTTL,
// otherwise refetches the releases list and refreshes the cache. Only the HTTP result is
// cached; callers evaluate the cooldown with selectRelease against their own clock.
// A cache with no candidates (including the legacy format) or a checked_at in the future is a miss.
func releaseCandidates(ctx context.Context, releaseURL, statePath string, now time.Time) ([]releaseCandidate, error) {
	s, _ := loadState(statePath) // missing or corrupt cache is a miss

	if len(s.Candidates) > 0 && !s.CheckedAt.IsZero() && !s.CheckedAt.After(now) && now.Sub(s.CheckedAt) < cacheTTL {
		return append([]releaseCandidate(nil), s.Candidates...), nil
	}

	releases, err := fetchReleases(ctx, releaseURL)
	if err != nil {
		// Record the failure only when there is a cache to reuse, so an empty
		// cache does not suppress retries for the full TTL.
		if len(s.Candidates) > 0 {
			_ = saveState(statePath, state{CheckedAt: now, LatestVersion: s.LatestVersion, Candidates: s.Candidates})
		}

		return nil, fmt.Errorf("refresh release candidates: %w", err)
	}

	candidates := candidatesFromReleases(releases)

	latest := ""
	if eligible, _ := selectRelease(candidates, now, false); eligible != nil {
		latest = eligible.Tag
	}

	// The cache is best effort: a failed save must not hide the fetched releases.
	_ = saveState(statePath, state{CheckedAt: now, LatestVersion: latest, Candidates: candidates})

	return candidates, nil
}

func checkAsync(ctx context.Context, version, releaseURL, statePath string) <-chan *Result {
	if !shouldCheck(version) {
		ch := make(chan *Result, 1)
		ch <- nil
		close(ch)
		return ch
	}

	ch := make(chan *Result, 1)

	go func() {
		defer close(ch)
		defer func() {
			if r := recover(); r != nil {
				ch <- nil
			}
		}()

		currentV, err := goversion.NewVersion(version)
		if err != nil || currentV.Prerelease() != "" {
			ch <- nil
			return
		}

		now := time.Now()

		candidates, err := releaseCandidates(ctx, releaseURL, statePath, now)
		if err != nil {
			ch <- nil
			return
		}

		eligible, pending := selectRelease(candidates, now, false)
		if eligible == nil {
			ch <- nil
			return
		}

		latestV, err := goversion.NewVersion(eligible.Tag)
		if err != nil || !currentV.LessThan(latestV) {
			ch <- nil
			return
		}

		res := &Result{Current: version, Latest: eligible.Tag}
		if pending != nil {
			res.Pending = pending.Tag
		}

		ch <- res
	}()

	return ch
}

func CheckAsync(ctx context.Context, version string) <-chan *Result {
	return checkAsync(ctx, version, DefaultReleaseURL, defaultStatePath())
}

func FormatNotice(current, latest string) string {
	currentColored := color.New(color.FgYellow).Sprint(current)
	latestColored := color.New(color.FgGreen).Sprint(latest)
	urlColored := color.New(color.FgCyan).Sprint(releaseTagPageURL + url.PathEscape(latest))

	return fmt.Sprintf(
		"\nA new release of jailoc is available: %s → %s\n%s\n",
		currentColored,
		latestColored,
		urlColored,
	)
}
