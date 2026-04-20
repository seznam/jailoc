package update

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
				data, err := json.Marshal(state{CheckedAt: checkedAt, LatestVersion: "v2.0.0"})
				if err != nil {
					t.Fatalf("marshal state: %v", err)
				}
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

		if err := saveState(path, state{CheckedAt: time.Now(), LatestVersion: "v9.9.9"}); err != nil {
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
		if _, ok := raw["CheckedAt"]; ok {
			t.Fatal("unexpected CheckedAt key")
		}
		if _, ok := raw["LatestVersion"]; ok {
			t.Fatal("unexpected LatestVersion key")
		}
	})
}

func TestCheckLatest(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		got, err := checkLatest(t.Context(), ts.URL)
		if err != nil {
			t.Fatalf("checkLatest returned error: %v", err)
		}
		if got != "v2.0.0" {
			t.Fatalf("checkLatest = %q, want %q", got, "v2.0.0")
		}
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		_, err := checkLatest(t.Context(), ts.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertContains(t, err.Error(), "unexpected status 500")
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not json"))
		}))
		t.Cleanup(ts.Close)

		_, err := checkLatest(t.Context(), ts.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertContains(t, err.Error(), "decode release response")
	})

	t.Run("context cancelled", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := checkLatest(ctx, ts.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertContains(t, err.Error(), "execute release request")
	})

	t.Run("missing tag_name", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"other_field":"value"}`))
		}))
		t.Cleanup(ts.Close)

		_, err := checkLatest(t.Context(), ts.URL)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		assertContains(t, err.Error(), "missing tag_name")
	})
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
	if !strings.Contains(notice, "github.com/seznam/jailoc/releases/latest") {
		t.Fatalf("notice does not contain release URL: %q", notice)
	}
	if !strings.Contains(notice, "new release") {
		t.Fatalf("notice does not contain 'new release': %q", notice)
	}
}

func TestCheckAsync(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "")

	t.Run("happy path", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v99.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if res.Current != "v1.0.0" || res.Latest != "v99.0.0" {
			t.Fatalf("unexpected result: %+v", res)
		}
	})

	t.Run("up to date", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("ahead of latest", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v2.0.0", ts.URL, statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("gated dev version does not hit server", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v99.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "dev", ts.URL, statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
		if calls.Load() != 0 {
			t.Fatalf("expected server to not be hit, calls=%d", calls.Load())
		}
	})

	t.Run("gated CI env", func(t *testing.T) {
		t.Setenv("CI", "true")

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", "http://127.0.0.1:1", statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("gated notifier env", func(t *testing.T) {
		t.Setenv("JAILOC_NO_UPDATE_NOTIFIER", "1")

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", "http://127.0.0.1:1", statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("network error returns nil", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v99.0.0"}`))
		}))
		ts.Close()

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
		if res != nil {
			t.Fatalf("expected nil result, got %+v", res)
		}
	})

	t.Run("cache hit within 24h uses cached value", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		s := state{CheckedAt: time.Now(), LatestVersion: "v2.0.0"}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal state: %v", err)
		}
		if err := os.WriteFile(statePath, data, 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write state file: %v", err)
		}

		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
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

	t.Run("cache expired fetches fresh value", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v3.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		s := state{CheckedAt: time.Now().Add(-25 * time.Hour), LatestVersion: "v2.0.0"}
		data, err := json.Marshal(s)
		if err != nil {
			t.Fatalf("marshal state: %v", err)
		}
		if err := os.WriteFile(statePath, data, 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write state file: %v", err)
		}

		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
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

	t.Run("corrupt state falls through to fetch", func(t *testing.T) {
		var calls atomic.Int32
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			calls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v4.0.0"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil { //nolint:gosec // test file in temp directory
			t.Fatalf("write corrupt state file: %v", err)
		}

		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
		if res == nil {
			t.Fatal("expected non-nil result")
		}
		if calls.Load() != 1 {
			t.Fatalf("expected one HTTP request with corrupt state, calls=%d", calls.Load())
		}
	})

	t.Run("pre release latest is skipped", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"tag_name":"v2.0.0-beta.1"}`))
		}))
		t.Cleanup(ts.Close)

		statePath := filepath.Join(t.TempDir(), "update-state.json")
		res := recvResult(t, checkAsync(context.Background(), "v1.0.0", ts.URL, statePath))
		if res != nil {
			t.Fatalf("expected nil result for prerelease, got %+v", res)
		}
	})
}
