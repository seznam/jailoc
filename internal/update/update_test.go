package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

func recvResult(t *testing.T, ch <-chan *Result) *Result {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	select {
	case res := <-ch:
		return res
	case <-ctx.Done():
		t.Fatal("checkAsync timed out")
		return nil
	}
}

func TestShouldCheck(t *testing.T) {
	tests := []struct {
		name    string
		version string
		envs    map[string]string
		want    bool
	}{
		{name: "dev version", version: "dev", want: false},
		{name: "devel version", version: "(devel)", want: false},
		{name: "CI env set", version: "v1.0.0", envs: map[string]string{"CI": "true"}, want: false},
		{name: "notifier disabled", version: "v1.0.0", envs: map[string]string{"JAILOC_NO_UPDATE_NOTIFIER": "1"}, want: false},
		{name: "normal version", version: "v1.0.0", want: true},
		{name: "stable semver", version: "v0.8.0", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CI", "")
			t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "")
			for k, v := range tt.envs {
				t.Setenv(k, v)
			}

			got := shouldCheck(tt.version)
			if got != tt.want {
				t.Fatalf("shouldCheck(%q) = %v, want %v", tt.version, got, tt.want)
			}
		})
	}
}

func TestLoadState(t *testing.T) {
	t.Parallel()

	type expectation struct {
		want     state
		wantZero bool
		wantErr  bool
	}

	checkedAt := time.Now().UTC().Round(0)
	candidates := []releaseCandidate{{Tag: "v2.0.0", PublishedAt: checkedAt.Add(-48 * time.Hour)}, {Tag: "v2.1.0-rc.1", PublishedAt: checkedAt, Prerelease: true}}

	tests := []struct {
		name  string
		setup func(t *testing.T, path string)
		exp   expectation
	}{
		{
			name: "missing file returns zero state and error",
			setup: func(_ *testing.T, _ string) {
			},
			exp: expectation{wantZero: true, wantErr: true},
		},
		{
			name: "valid json returns parsed state",
			setup: func(t *testing.T, path string) {
				t.Helper()
				data, err := json.Marshal(state{CheckedAt: checkedAt, LatestVersion: "v2.0.0", Candidates: candidates})
				if err != nil {
					t.Fatalf("marshal state: %v", err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test file in temp directory
					t.Fatalf("write state file: %v", err)
				}
			},
			exp: expectation{want: state{CheckedAt: checkedAt, LatestVersion: "v2.0.0", Candidates: candidates}},
		},
		{
			name: "legacy state without candidates loads with none",
			setup: func(t *testing.T, path string) {
				t.Helper()
				data := []byte(`{"checked_at":"` + checkedAt.Format(time.RFC3339Nano) + `","latest_version":"v2.0.0"}`)
				if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test file in temp directory
					t.Fatalf("write state file: %v", err)
				}
			},
			exp: expectation{want: state{CheckedAt: checkedAt, LatestVersion: "v2.0.0"}},
		},
		{
			name: "corrupt json returns zero state and error",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil { //nolint:gosec // test file in temp directory
					t.Fatalf("write corrupt state file: %v", err)
				}
			},
			exp: expectation{wantZero: true, wantErr: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			path := filepath.Join(dir, "state.json")
			tt.setup(t, path)

			got, err := loadState(path)
			if tt.exp.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tt.exp.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.exp.wantZero {
				if !got.CheckedAt.IsZero() || got.LatestVersion != "" {
					t.Fatalf("expected zero state, got %+v", got)
				}
				return
			}

			if !got.CheckedAt.Equal(tt.exp.want.CheckedAt) {
				t.Fatalf("CheckedAt = %v, want %v", got.CheckedAt, tt.exp.want.CheckedAt)
			}
			if got.LatestVersion != tt.exp.want.LatestVersion {
				t.Fatalf("LatestVersion = %q, want %q", got.LatestVersion, tt.exp.want.LatestVersion)
			}
			if len(got.Candidates) != len(tt.exp.want.Candidates) {
				t.Fatalf("Candidates = %+v, want %+v", got.Candidates, tt.exp.want.Candidates)
			}
			for i, c := range tt.exp.want.Candidates {
				g := got.Candidates[i]
				if g.Tag != c.Tag || !g.PublishedAt.Equal(c.PublishedAt) || g.Prerelease != c.Prerelease {
					t.Fatalf("Candidates[%d] = %+v, want %+v", i, g, c)
				}
			}
		})
	}
}

func TestSaveState(t *testing.T) {
	t.Parallel()

	t.Run("saves and reloads", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "update-state.json")
		want := state{CheckedAt: time.Now().UTC().Round(0), LatestVersion: "v3.1.4"}

		if err := saveState(path, want); err != nil {
			t.Fatalf("saveState: %v", err)
		}

		data, err := os.ReadFile(path) //nolint:gosec // test reads file in temp directory
		if err != nil {
			t.Fatalf("read state file: %v", err)
		}

		var got state
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("unmarshal saved state: %v", err)
		}

		if !got.CheckedAt.Equal(want.CheckedAt) {
			t.Fatalf("CheckedAt = %v, want %v", got.CheckedAt, want.CheckedAt)
		}
		if got.LatestVersion != want.LatestVersion {
			t.Fatalf("LatestVersion = %q, want %q", got.LatestVersion, want.LatestVersion)
		}
	})

	t.Run("creates parent directory", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "nested", "cache", "state.json")

		if err := saveState(path, state{CheckedAt: time.Now(), LatestVersion: "v1.2.3"}); err != nil {
			t.Fatalf("saveState: %v", err)
		}

		if _, err := os.Stat(filepath.Dir(path)); err != nil {
			t.Fatalf("parent dir was not created: %v", err)
		}
	})

	t.Run("returns error when parent cannot be created", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		blockingFile := filepath.Join(dir, "file")
		if err := os.WriteFile(blockingFile, []byte("x"), 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write blocking file: %v", err)
		}

		path := filepath.Join(blockingFile, "state.json")
		err := saveState(path, state{CheckedAt: time.Now(), LatestVersion: "v1.2.3"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertContains(t, err.Error(), "create state directory")
	})

	t.Run("writes expected json tags", func(t *testing.T) {
		t.Parallel()

		dir := t.TempDir()
		path := filepath.Join(dir, "state.json")

		if err := saveState(path, state{CheckedAt: time.Now(), LatestVersion: "v9.9.9", Candidates: []releaseCandidate{{Tag: "v9.9.9", PublishedAt: time.Now(), Prerelease: true}}}); err != nil {
			t.Fatalf("saveState: %v", err)
		}

		data, err := os.ReadFile(path) //nolint:gosec // test reads file in temp directory
		if err != nil {
			t.Fatalf("read state file: %v", err)
		}

		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("unmarshal json map: %v", err)
		}

		if _, ok := raw["checked_at"]; !ok {
			t.Fatal("expected checked_at key")
		}
		if _, ok := raw["latest_version"]; !ok {
			t.Fatal("expected latest_version key")
		}
		list, ok := raw["candidates"].([]any)
		if !ok || len(list) != 1 {
			t.Fatalf("expected one candidates entry, got %v", raw["candidates"])
		}
		cand, ok := list[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected candidate shape: %v", list[0])
		}
		for _, key := range []string{"tag", "published_at", "prerelease"} {
			if _, ok := cand[key]; !ok {
				t.Fatalf("expected candidate key %q in %v", key, cand)
			}
		}
		if _, ok := raw["CheckedAt"]; ok {
			t.Fatal("unexpected CheckedAt key")
		}
		if _, ok := raw["LatestVersion"]; ok {
			t.Fatal("unexpected LatestVersion key")
		}
	})
}

func TestLoadStateWrapsErrors(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	_, err := loadState(filepath.Join(dir, "missing.json"))
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err.Error(), "read state file")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("expected wrapped fs.ErrNotExist, got %v", err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("nope"), 0o600); err != nil { //nolint:gosec // test file in temp directory
		t.Fatalf("write: %v", err)
	}
	_, err = loadState(bad)
	if err == nil {
		t.Fatal("expected error")
	}
	assertContains(t, err.Error(), "decode state file")
}

func TestFormatNotice(t *testing.T) {
	t.Parallel()

	notice := FormatNotice("v1.0.0", "v2.0.0")
	if !strings.Contains(notice, "v1.0.0") {
		t.Fatalf("notice does not contain current version: %q", notice)
	}
	if !strings.Contains(notice, "v2.0.0") {
		t.Fatalf("notice does not contain latest version: %q", notice)
	}
	if !strings.Contains(notice, "github.com/seznam/jailoc/releases/tag/v2.0.0") {
		t.Fatalf("notice does not contain tag-specific release URL: %q", notice)
	}
	if strings.Contains(notice, "releases/latest") {
		t.Fatalf("notice must not point at releases/latest: %q", notice)
	}
	if !strings.Contains(notice, "new release") {
		t.Fatalf("notice does not contain 'new release': %q", notice)
	}
}

func TestDefaultReleaseURLIsListEndpoint(t *testing.T) {
	t.Parallel()

	if DefaultReleaseURL != "https://api.github.com/repos/seznam/jailoc/releases" {
		t.Fatalf("DefaultReleaseURL = %q", DefaultReleaseURL)
	}
}

// ago formats a publication time relative to the real clock for checkAsync tests.
func ago(d time.Duration) time.Time {
	return time.Now().Add(-d).UTC().Round(time.Second)
}

func listBody(releases ...string) string {
	return "[" + strings.Join(releases, ",") + "]"
}

func rel(tag string, age time.Duration) string {
	return releaseJSON(tag, ago(age))
}

// countingServer serves body for every request and counts requests.
func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)

	return ts, &calls
}

func writeState(t *testing.T, path string, s state) {
	t.Helper()

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil { //nolint:gosec // test file in temp directory
		t.Fatalf("write state file: %v", err)
	}
}

func TestReleaseCandidates(t *testing.T) {
	t.Parallel()

	published := testNow.Add(-time.Hour)
	body := listBody(releaseJSON("v2.0.0", published), releaseJSON("v1.0.0", testNow.Add(-72*time.Hour)))

	t.Run("fetch saves candidates and eligible latest_version", func(t *testing.T) {
		t.Parallel()

		ts, calls := countingServer(t, body)
		statePath := filepath.Join(t.TempDir(), "state.json")

		got, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow)
		if err != nil {
			t.Fatalf("releaseCandidates: %v", err)
		}
		if len(got) != 2 || calls.Load() != 1 {
			t.Fatalf("got %+v, calls=%d", got, calls.Load())
		}

		s, err := loadState(statePath)
		if err != nil {
			t.Fatalf("loadState: %v", err)
		}
		if !s.CheckedAt.Equal(testNow) || s.LatestVersion != "v1.0.0" || len(s.Candidates) != 2 {
			t.Fatalf("saved state = %+v", s)
		}
	})

	t.Run("cooldown is re-evaluated against now without HTTP", func(t *testing.T) {
		t.Parallel()

		ts, calls := countingServer(t, body)
		statePath := filepath.Join(t.TempDir(), "state.json")

		if _, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow); err != nil {
			t.Fatalf("initial fetch: %v", err)
		}

		// 22h later v2.0.0 is 23h old: still cooling down.
		early := testNow.Add(22 * time.Hour)
		cached, err := releaseCandidates(t.Context(), ts.URL, statePath, early)
		if err != nil {
			t.Fatalf("early: %v", err)
		}
		eligible, pending := selectRelease(cached, early, false)
		if tagOf(eligible) != "v1.0.0" || tagOf(pending) != "v2.0.0" {
			t.Fatalf("early: eligible=%q pending=%q", tagOf(eligible), tagOf(pending))
		}

		// 23h later v2.0.0 is exactly 24h old, still inside the 24h HTTP TTL.
		later := testNow.Add(23 * time.Hour)
		cached, err = releaseCandidates(t.Context(), ts.URL, statePath, later)
		if err != nil {
			t.Fatalf("later: %v", err)
		}
		eligible, pending = selectRelease(cached, later, false)
		if tagOf(eligible) != "v2.0.0" || pending != nil {
			t.Fatalf("later: eligible=%q pending=%q", tagOf(eligible), tagOf(pending))
		}

		if calls.Load() != 1 {
			t.Fatalf("HTTP calls = %d, want 1", calls.Load())
		}
	})

	t.Run("TTL expiry refetches", func(t *testing.T) {
		t.Parallel()

		ts, calls := countingServer(t, body)
		statePath := filepath.Join(t.TempDir(), "state.json")
		writeState(t, statePath, state{CheckedAt: testNow, Candidates: []releaseCandidate{cand("v0.1.0", 100*time.Hour)}})

		// just inside TTL: cache hit
		if _, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow.Add(cacheTTL-time.Nanosecond)); err != nil {
			t.Fatalf("inside TTL: %v", err)
		}
		if calls.Load() != 0 {
			t.Fatalf("calls inside TTL = %d", calls.Load())
		}

		// exactly TTL: miss
		got, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow.Add(cacheTTL))
		if err != nil {
			t.Fatalf("at TTL: %v", err)
		}
		if calls.Load() != 1 || len(got) != 2 {
			t.Fatalf("calls=%d got=%+v", calls.Load(), got)
		}
	})

	t.Run("misses: legacy, future checked_at, zero checked_at, no candidates", func(t *testing.T) {
		t.Parallel()

		cases := map[string]state{
			"legacy":          {CheckedAt: testNow, LatestVersion: "v1.0.0"},
			"future":          {CheckedAt: testNow.Add(time.Hour), LatestVersion: "v1.0.0", Candidates: []releaseCandidate{cand("v1.0.0", 48*time.Hour)}},
			"zero checked_at": {LatestVersion: "v1.0.0", Candidates: []releaseCandidate{cand("v1.0.0", 48*time.Hour)}},
			"empty":           {CheckedAt: testNow, LatestVersion: "v1.0.0", Candidates: []releaseCandidate{}},
		}
		for name, s := range cases {
			ts, calls := countingServer(t, body)
			statePath := filepath.Join(t.TempDir(), "state.json")
			writeState(t, statePath, s)

			if _, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("%s: calls = %d, want 1", name, calls.Load())
			}
		}
	})

	t.Run("failure with cached candidates refreshes checked_at and keeps them", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "state.json")
		old := cand("v1.0.0", 100*time.Hour)
		writeState(t, statePath, state{CheckedAt: testNow.Add(-48 * time.Hour), LatestVersion: "v1.0.0", Candidates: []releaseCandidate{old}})

		if _, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow); err == nil {
			t.Fatal("expected error")
		}

		s, err := loadState(statePath)
		if err != nil {
			t.Fatalf("loadState: %v", err)
		}
		if !s.CheckedAt.Equal(testNow) || s.LatestVersion != "v1.0.0" || len(s.Candidates) != 1 || s.Candidates[0].Tag != "v1.0.0" {
			t.Fatalf("state after failure = %+v", s)
		}
	})

	t.Run("failure without cache writes no state", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "state.json")
		if _, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow); err == nil {
			t.Fatal("expected error")
		}
		if _, err := os.Stat(statePath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("state file should not exist, stat err = %v", err)
		}
	})

	t.Run("unwritable cache does not fail the fetch", func(t *testing.T) {
		t.Parallel()

		ts, _ := countingServer(t, body)
		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write: %v", err)
		}

		got, err := releaseCandidates(t.Context(), ts.URL, filepath.Join(blocker, "state.json"), testNow)
		if err != nil {
			t.Fatalf("releaseCandidates: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("returned slice is a copy of the cache", func(t *testing.T) {
		t.Parallel()

		ts, _ := countingServer(t, body)
		statePath := filepath.Join(t.TempDir(), "state.json")
		writeState(t, statePath, state{CheckedAt: testNow, Candidates: []releaseCandidate{cand("v1.0.0", 48*time.Hour)}})

		got, err := releaseCandidates(t.Context(), ts.URL, statePath, testNow)
		if err != nil || len(got) != 1 || got[0].Tag != "v1.0.0" {
			t.Fatalf("got %+v err %v", got, err)
		}
	})
}

func TestCheckAsync(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "")

	check := func(t *testing.T, version, url, statePath string) *Result {
		t.Helper()
		return recvResult(t, checkAsync(context.Background(), version, url, statePath))
	}

	t.Run("happy path", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v99.0.0", 48*time.Hour)))

		res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json"))
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if res.Current != "v1.0.0" || res.Latest != "v99.0.0" || res.Pending != "" {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("highest eligible stable wins over listing order and cooling releases", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(
			rel("v1.5.1", 72*time.Hour),
			rel("v3.0.0", time.Hour),
			rel("v2.0.0", 48*time.Hour),
			rel("v4.0.0-rc.1", 72*time.Hour),
			releaseJSON("v5.0.0", time.Now().Add(time.Hour)),
			`{"tag_name":"v6.0.0","published_at":"garbage"}`,
		))

		res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json"))
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if res.Latest != "v2.0.0" || res.Pending != "v3.0.0" {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("only cooling release yields no result", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v2.0.0", time.Hour)))

		if res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v1.0.0", 48*time.Hour)))

		if res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("ahead of latest", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v1.0.0", 48*time.Hour)))

		if res := check(t, "v2.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("gated dev version does not hit server", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v99.0.0", 48*time.Hour)))

		if res := check(t, "dev", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
		if calls.Load() != 0 {
			t.Fatalf("expected server to not be hit, calls=%d", calls.Load())
		}
	})

	t.Run("gated CI env", func(t *testing.T) {
		t.Setenv("CI", "true")

		if res := check(t, "v1.0.0", "http://127.0.0.1:1", filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("gated notifier env", func(t *testing.T) {
		t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "1")

		if res := check(t, "v1.0.0", "http://127.0.0.1:1", filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("current prerelease does not hit server", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v99.0.0", 48*time.Hour)))

		if res := check(t, "v1.0.0-rc.1", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
		if calls.Load() != 0 {
			t.Fatalf("calls=%d", calls.Load())
		}
	})

	t.Run("network error returns nil", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		ts.Close()

		if res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("cache hit within 24h uses cached candidates", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v9.9.9", 48*time.Hour)))

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		writeState(t, statePath, state{
			CheckedAt:     time.Now(),
			LatestVersion: "v2.0.0",
			Candidates:    []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(48 * time.Hour)}},
		})

		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if res.Latest != "v2.0.0" {
			t.Fatalf("Latest = %q, want %q", res.Latest, "v2.0.0")
		}
		if calls.Load() != 0 {
			t.Fatalf("expected no HTTP request on cache hit, calls=%d", calls.Load())
		}
	})

	t.Run("cache hit evaluates release age not cache age", func(t *testing.T) {
		ts, calls := countingServer(t, listBody())

		// Fetched 2h ago when v2.0.0 was 1h old (cooling): now 3h old, still cooling -> nil.
		cooling := filepath.Join(t.TempDir(), "cooling.json")
		writeState(t, cooling, state{CheckedAt: ago(2 * time.Hour), Candidates: []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(3 * time.Hour)}}})
		if res := check(t, "v1.0.0", ts.URL, cooling); res != nil {
			t.Fatalf("expected nil for cooling release, got %+v", res)
		}

		// Fetched 1h ago; v2.0.0 was published 25h ago, so it is eligible now.
		elapsed := filepath.Join(t.TempDir(), "elapsed.json")
		writeState(t, elapsed, state{CheckedAt: ago(time.Hour), Candidates: []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(25 * time.Hour)}}})
		res := check(t, "v1.0.0", ts.URL, elapsed)
		if res == nil || res.Latest != "v2.0.0" {
			t.Fatalf("expected v2.0.0, got %+v", res)
		}

		if calls.Load() != 0 {
			t.Fatalf("expected no HTTP requests, calls=%d", calls.Load())
		}
	})

	t.Run("cache expired fetches fresh value", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v3.0.0", 48*time.Hour)))

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		writeState(t, statePath, state{
			CheckedAt:     time.Now().Add(-25 * time.Hour),
			LatestVersion: "v2.0.0",
			Candidates:    []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(100 * time.Hour)}},
		})

		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if res.Latest != "v3.0.0" {
			t.Fatalf("Latest = %q, want %q", res.Latest, "v3.0.0")
		}
		if calls.Load() != 1 {
			t.Fatalf("expected one HTTP request on cache expiry, calls=%d", calls.Load())
		}
	})

	t.Run("legacy cache without candidates refreshes", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v4.0.0", 48*time.Hour)))

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		writeState(t, statePath, state{CheckedAt: time.Now(), LatestVersion: "v2.0.0"})

		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil || res.Latest != "v4.0.0" {
			t.Fatalf("unexpected result: %+v", res)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", calls.Load())
		}

		s, err := loadState(statePath)
		if err != nil || len(s.Candidates) != 1 || s.LatestVersion != "v4.0.0" {
			t.Fatalf("state = %+v err %v", s, err)
		}
	})

	t.Run("future checked_at refreshes", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v4.0.0", 48*time.Hour)))

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		writeState(t, statePath, state{
			CheckedAt:  time.Now().Add(time.Hour),
			Candidates: []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(48 * time.Hour)}},
		})

		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil || res.Latest != "v4.0.0" || calls.Load() != 1 {
			t.Fatalf("res=%+v calls=%d", res, calls.Load())
		}
	})

	t.Run("corrupt state falls through to fetch", func(t *testing.T) {
		ts, calls := countingServer(t, listBody(rel("v4.0.0", 48*time.Hour)))

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write corrupt state file: %v", err)
		}

		if res := check(t, "v1.0.0", ts.URL, statePath); res == nil {
			t.Fatal("expected non-nil result")
		}
		if calls.Load() != 1 {
			t.Fatalf("expected one HTTP request with corrupt state, calls=%d", calls.Load())
		}
	})

	t.Run("fetch failure with cache suppresses retries for the TTL", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		writeState(t, statePath, state{
			CheckedAt:     time.Now().Add(-48 * time.Hour),
			LatestVersion: "v2.0.0",
			Candidates:    []releaseCandidate{{Tag: "v2.0.0", PublishedAt: ago(100 * time.Hour)}},
		})

		if res := check(t, "v1.0.0", ts.URL, statePath); res != nil {
			t.Fatalf("expected nil result on failure, got %+v", res)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls = %d, want 1", calls.Load())
		}

		// The failure refreshed checked_at, so the next check serves the cache without HTTP.
		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil || res.Latest != "v2.0.0" {
			t.Fatalf("expected cached v2.0.0, got %+v", res)
		}
		if calls.Load() != 1 {
			t.Fatalf("calls after second check = %d, want 1", calls.Load())
		}
	})

	t.Run("fetch failure without cache keeps retrying", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		check(t, "v1.0.0", ts.URL, statePath)
		check(t, "v1.0.0", ts.URL, statePath)
		if calls.Load() != 2 {
			t.Fatalf("calls = %d, want 2", calls.Load())
		}
	})

	t.Run("unwritable state still reports update", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v4.0.0", 48*time.Hour)))

		blocker := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write: %v", err)
		}

		res := check(t, "v1.0.0", ts.URL, filepath.Join(blocker, "state.json"))
		if res == nil || res.Latest != "v4.0.0" {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("pre release latest is skipped", func(t *testing.T) {
		ts, _ := countingServer(t, listBody(rel("v2.0.0-beta.1", 48*time.Hour)))

		if res := check(t, "v1.0.0", ts.URL, filepath.Join(t.TempDir(), "update-state.json")); res != nil {
			t.Fatalf("expected nil result for prerelease, got %+v", res)
		}
	})

	t.Run("reads all pages and caches up to 50 candidates", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			page, _ := strconv.Atoi(r.URL.Query().Get("page"))
			items := make([]string, 0, 10)
			for i := range 10 {
				items = append(items, rel(fmt.Sprintf("v%d.%d.0", page, i), 48*time.Hour))
			}
			_, _ = w.Write([]byte(listBody(items...)))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := check(t, "v1.0.0", ts.URL, statePath)
		if res == nil || res.Latest != "v5.9.0" {
			t.Fatalf("expected highest release from last page, got %+v", res)
		}
		if calls.Load() != 5 {
			t.Fatalf("calls = %d, want 5", calls.Load())
		}

		s, err := loadState(statePath)
		if err != nil {
			t.Fatalf("loadState: %v", err)
		}
		if len(s.Candidates) != 50 || s.LatestVersion != "v5.9.0" {
			t.Fatalf("state: %d candidates, latest %q", len(s.Candidates), s.LatestVersion)
		}
	})
}
