package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	goversion "github.com/hashicorp/go-version"
)

const (
	DefaultReleaseURL = "https://api.github.com/repos/seznam/jailoc/releases/latest"
	cacheTTL          = 24 * time.Hour
	httpTimeout       = 10 * time.Second
	bodyLimit         = int64(1 << 20)
	releasePageURL    = "https://github.com/seznam/jailoc/releases/latest"
)

type state struct {
	CheckedAt     time.Time `json:"checked_at"`
	LatestVersion string    `json:"latest_version"`
}

type Result struct {
	Current string
	Latest  string
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
		return state{}, err
	}

	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return state{}, err
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

func checkLatest(ctx context.Context, releaseURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("create release request for %s: %w", releaseURL, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "jailoc-update-checker")

	client := &http.Client{Timeout: httpTimeout}

	resp, err := client.Do(req) //nolint:gosec // releaseURL is caller-provided and defaults to constant GitHub API endpoint
	if err != nil {
		return "", fmt.Errorf("execute release request to %s: %w", releaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch latest release from %s: unexpected status %d", releaseURL, resp.StatusCode)
	}

	lr := io.LimitReader(resp.Body, bodyLimit+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return "", fmt.Errorf("read release body from %s: %w", releaseURL, err)
	}

	if int64(len(data)) > bodyLimit {
		return "", fmt.Errorf("release response from %s exceeds 1MiB limit", releaseURL)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", fmt.Errorf("decode release response from %s: %w", releaseURL, err)
	}

	if payload.TagName == "" {
		return "", fmt.Errorf("decode release response from %s: missing tag_name", releaseURL)
	}

	return payload.TagName, nil
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

		s, _ := loadState(statePath)

		latest := s.LatestVersion
		fetched := false
		if s.LatestVersion == "" || s.CheckedAt.IsZero() || time.Until(s.CheckedAt) > 0 || time.Since(s.CheckedAt) >= cacheTTL {
			result, err := checkLatest(ctx, releaseURL)
			if err != nil {
				// Save state on failure only when there is a cached version to reuse,
				// so an empty cache does not suppress retries for the full TTL.
				if s.LatestVersion != "" {
					_ = saveState(statePath, state{CheckedAt: time.Now(), LatestVersion: s.LatestVersion})
				}
				ch <- nil
				return
			}

			latest = result
			fetched = true
		}

		currentV, err := goversion.NewVersion(version)
		if err != nil {
			ch <- nil
			return
		}

		latestV, err := goversion.NewVersion(latest)
		if err != nil {
			ch <- nil
			return
		}

		if fetched {
			_ = saveState(statePath, state{CheckedAt: time.Now(), LatestVersion: latest})
		}

		if currentV.Prerelease() != "" || latestV.Prerelease() != "" {
			ch <- nil
			return
		}

		if currentV.LessThan(latestV) {
			ch <- &Result{Current: version, Latest: latest}
			return
		}

		ch <- nil
	}()

	return ch
}

func CheckAsync(ctx context.Context, version string) <-chan *Result {
	return checkAsync(ctx, version, DefaultReleaseURL, defaultStatePath())
}

func FormatNotice(current, latest string) string {
	currentColored := color.New(color.FgYellow).Sprint(current)
	latestColored := color.New(color.FgGreen).Sprint(latest)
	urlColored := color.New(color.FgCyan).Sprint(releasePageURL)

	return fmt.Sprintf(
		"\nA new release of jailoc is available: %s → %s\n%s\n",
		currentColored,
		latestColored,
		urlColored,
	)
}
