package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var suNow = time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

const (
	suOldBinary = "OLD-BINARY"
	suNewBinary = "NEW-BINARY"
)

func suRelease(tag string, age time.Duration, mutate ...func(*githubRelease)) githubRelease {
	r := githubRelease{
		TagName:     tag,
		PublishedAt: suNow.Add(-age),
		Assets: []githubAsset{{
			Name: archiveAssetName(tag), Size: 100, Digest: "sha256:" + testHexA,
			BrowserDownloadURL: "https://example.invalid/" + archiveAssetName(tag),
		}},
	}
	for _, m := range mutate {
		m(&r)
	}

	return r
}

type fakeAPI struct {
	list     []githubRelease
	tags     map[string]githubRelease // exact-tag responses that differ from the list
	listHits atomic.Int32
	tagHits  atomic.Int32
}

func (a *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/releases":
		a.listHits.Add(1)
		_ = json.NewEncoder(w).Encode(a.list)
	case strings.HasPrefix(r.URL.Path, "/releases/tags/"):
		a.tagHits.Add(1)
		tag := strings.TrimPrefix(r.URL.Path, "/releases/tags/")
		if rel, ok := a.tags[tag]; ok {
			_ = json.NewEncoder(w).Encode(rel)

			return
		}
		for _, rel := range a.list {
			if rel.TagName == tag {
				_ = json.NewEncoder(w).Encode(rel)

				return
			}
		}
		http.NotFound(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *fakeAPI) requests() int32 { return a.listHits.Load() + a.tagHits.Load() }

type suFixture struct {
	deps      deps
	api       *fakeAPI
	exe       string
	state     string
	fetched   atomic.Int32
	fetchName atomic.Value
	result    verifyResult
}

func newSUFixture(t *testing.T, api *fakeAPI, exeSubdirs ...string) *suFixture {
	t.Helper()

	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	dir := filepath.Join(append([]string{t.TempDir()}, exeSubdirs...)...)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "jailoc")
	if err := os.WriteFile(exe, []byte(suOldBinary), 0o755); err != nil { //nolint:gosec // the test executable must be executable
		t.Fatal(err)
	}

	f := &suFixture{api: api, exe: exe, state: filepath.Join(t.TempDir(), "update-state.json")}
	f.deps = deps{
		releaseURL: srv.URL + "/releases",
		statePath:  f.state,
		now:        func() time.Time { return suNow },
		executable: func() (string, error) { return exe, nil },
		replace:    replaceExecutable,
		fetchArchive: func(_ context.Context, _ githubRelease, asset githubAsset, dir string) (*os.File, verifyResult, error) {
			f.fetched.Add(1)
			f.fetchName.Store(asset.Name)

			file, err := os.CreateTemp(dir, "archive-*.tar.gz")
			if err != nil {
				return nil, verifyResult{}, err
			}
			if _, err := file.Write(tarGzBytes(t, []tarEntry{fileEntry("jailoc", suNewBinary)})); err != nil {
				return nil, verifyResult{}, err
			}

			return file, f.result, nil
		},
	}

	return f
}

func (f *suFixture) binary(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(f.exe)
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

func (f *suFixture) assertNoTemps(t *testing.T) {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(filepath.Dir(f.exe), tempPattern))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("leftover temporary files: %v", matches)
	}
}

func (f *suFixture) assertUntouched(t *testing.T) {
	t.Helper()

	if got := f.binary(t); got != suOldBinary {
		t.Errorf("executable = %q, want it untouched", got)
	}
	f.assertNoTemps(t)
}

func standardList() []githubRelease {
	return []githubRelease{
		suRelease("v1.0.5", 72*time.Hour),
		suRelease("v1.1.0", 48*time.Hour),
		suRelease("v1.2.0", 2*time.Hour),
		suRelease("v1.3.0-rc.1", 72*time.Hour, func(r *githubRelease) { r.Prerelease = true }),
		suRelease("v1.4.0", 72*time.Hour, func(r *githubRelease) { r.Draft = true }),
	}
}

func TestSelfUpdateInstallsHighestEligible(t *testing.T) {
	t.Parallel()

	f := newSUFixture(t, &fakeAPI{list: standardList()})
	f.result = verifyResult{Signature: true, Warning: "gh missing"}
	if err := os.WriteFile(f.state, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
	if err != nil {
		t.Fatalf("selfUpdate() error = %v", err)
	}

	wantPath, _ := filepath.EvalSymlinks(f.exe)
	if !rep.Installed || rep.Current != "v1.0.0" || rep.Target != "v1.1.0" || rep.Pending != "v1.2.0" {
		t.Errorf("report = %+v", rep)
	}
	if rep.Path != wantPath || !rep.VerifiedSignature || !strings.Contains(rep.Warning, "gh missing") {
		t.Errorf("report = %+v, want path %q, signature, gh warning", rep, wantPath)
	}
	if !rep.PublishedAt.Equal(suNow.Add(-48 * time.Hour)) {
		t.Errorf("PublishedAt = %v", rep.PublishedAt)
	}
	if got := f.binary(t); got != suNewBinary {
		t.Errorf("executable = %q, want %q", got, suNewBinary)
	}
	if got, _ := f.fetchName.Load().(string); got != archiveAssetName("v1.1.0") {
		t.Errorf("downloaded asset %q, want %q", got, archiveAssetName("v1.1.0"))
	}
	if f.api.tagHits.Load() != 1 {
		t.Errorf("exact tag metadata fetched %d times, want 1", f.api.tagHits.Load())
	}
	if _, err := os.Stat(f.state); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state file still present after install: %v", err)
	}
	f.assertNoTemps(t)
}

func TestArchiveAssetName(t *testing.T) {
	t.Parallel()

	want := "jailoc_1.2.3_" + runtime.GOOS + "_" + runtime.GOARCH + ".tar.gz"
	if got := archiveAssetName("v1.2.3"); got != want {
		t.Errorf("archiveAssetName() = %q, want %q", got, want)
	}
}

func TestSelfUpdateCheck(t *testing.T) {
	t.Parallel()

	t.Run("reports without writing or downloading", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{Check: true}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if rep.Installed || rep.Target != "v1.1.0" || rep.Pending != "v1.2.0" || rep.Path == "" || rep.Installation != "" {
			t.Errorf("report = %+v", rep)
		}
		if f.fetched.Load() != 0 {
			t.Error("check downloaded an archive")
		}
		f.assertUntouched(t)
	})

	t.Run("classifies unsupported installs without failing", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()}, "Caskroom", "jailoc")

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{Check: true}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if rep.Target != "v1.1.0" || !strings.Contains(rep.Installation, "Homebrew") {
			t.Errorf("report = %+v, want target and Homebrew classification", rep)
		}
		f.assertUntouched(t)
	})

	t.Run("explicit version reports exact metadata", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{Check: true, Version: "1.2.0"}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if rep.Target != "v1.2.0" || !rep.PublishedAt.Equal(suNow.Add(-2*time.Hour)) {
			t.Errorf("report = %+v", rep)
		}
		if f.api.listHits.Load() != 0 {
			t.Error("explicit version listed releases")
		}
	})
}

func TestSelfUpdateGuardsRunBeforeNetwork(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subdirs []string
		current string
		opts    Options
		wantErr string
	}{
		{name: "homebrew", subdirs: []string{"Caskroom", "jailoc"}, current: "v1.0.0", wantErr: "Homebrew"},
		{name: "dev build", current: "dev", wantErr: "not a release build"},
		{name: "invalid explicit version", current: "v1.0.0", opts: Options{Version: "latest"}, wantErr: "invalid release tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newSUFixture(t, &fakeAPI{list: standardList()}, tt.subdirs...)

			_, err := selfUpdate(context.Background(), tt.current, tt.opts, f.deps)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("selfUpdate() error = %v, want containing %q", err, tt.wantErr)
			}
			if f.api.requests() != 0 {
				t.Errorf("made %d requests before the guard failed", f.api.requests())
			}
			f.assertUntouched(t)
		})
	}

	t.Run("executable lookup failure", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		f.deps.executable = func() (string, error) { return "", errors.New("no exe") }

		if _, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps); err == nil {
			t.Fatal("selfUpdate() succeeded")
		}
		if f.api.requests() != 0 {
			t.Error("made requests without an executable")
		}
	})

	t.Run("force bypasses homebrew guard", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()}, "Caskroom", "jailoc")

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{Force: true}, f.deps)
		if err != nil || !rep.Installed {
			t.Fatalf("selfUpdate() = %+v, %v; want installed", rep, err)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		if _, err := selfUpdate(ctx, "v1.0.0", Options{}, f.deps); !errors.Is(err, context.Canceled) {
			t.Fatalf("selfUpdate() error = %v, want context.Canceled", err)
		}
		if f.api.requests() != 0 {
			t.Error("made requests with a canceled context")
		}
	})
}

func TestSelfUpdateSelection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		list          []githubRelease
		current       string
		opts          Options
		wantInstalled bool
		wantTarget    string
		wantPending   string
	}{
		{
			name:    "nothing eligible reports pending",
			list:    []githubRelease{suRelease("v1.2.0", 2*time.Hour)},
			current: "v1.0.0", wantPending: "v1.2.0",
		},
		{name: "empty list", current: "v1.0.0"},
		{
			name:    "equal version is not installed",
			list:    []githubRelease{suRelease("v1.1.0", 48*time.Hour)},
			current: "v1.1.0", wantTarget: "v1.1.0",
		},
		{
			name:    "equal version with bare current",
			list:    []githubRelease{suRelease("v1.1.0", 48*time.Hour)},
			current: "1.1.0", wantTarget: "v1.1.0",
		},
		{
			name:    "newer current is never downgraded",
			list:    []githubRelease{suRelease("v1.1.0", 48*time.Hour)},
			current: "v1.2.0",
		},
		{
			name:    "force reinstalls equal",
			list:    []githubRelease{suRelease("v1.1.0", 48*time.Hour)},
			current: "v1.1.0", opts: Options{Force: true}, wantInstalled: true, wantTarget: "v1.1.0",
		},
		{
			name:    "force does not downgrade",
			list:    []githubRelease{suRelease("v1.1.0", 48*time.Hour)},
			current: "v1.2.0", opts: Options{Force: true},
		},
		{
			name:    "no cooldown takes the newest",
			list:    standardList(),
			current: "v1.0.0", opts: Options{NoCooldown: true}, wantInstalled: true, wantTarget: "v1.2.0",
		},
		{
			name:    "pending older than current is hidden",
			list:    []githubRelease{suRelease("v1.1.0", 2*time.Hour)},
			current: "v1.2.0",
		},
		{
			name:    "force installs over a dev build",
			list:    standardList(),
			current: "dev", opts: Options{Force: true}, wantInstalled: true, wantTarget: "v1.1.0", wantPending: "v1.2.0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newSUFixture(t, &fakeAPI{list: tt.list})

			rep, err := selfUpdate(context.Background(), tt.current, tt.opts, f.deps)
			if err != nil {
				t.Fatalf("selfUpdate() error = %v", err)
			}
			if rep.Installed != tt.wantInstalled || rep.Target != tt.wantTarget || rep.Pending != tt.wantPending || rep.Current != tt.current {
				t.Errorf("report = %+v, want installed=%v target=%q pending=%q", rep, tt.wantInstalled, tt.wantTarget, tt.wantPending)
			}
			if !tt.wantInstalled {
				if f.fetched.Load() != 0 {
					t.Error("downloaded an archive without installing")
				}
				f.assertUntouched(t)
			}
		})
	}
}

func TestSelfUpdateExplicitVersion(t *testing.T) {
	t.Parallel()

	list := []githubRelease{
		suRelease("v1.0.0-rc.1", time.Hour, func(r *githubRelease) { r.Prerelease = true }),
		suRelease("v1.0.5", 72*time.Hour),
		suRelease("v1.1.0", time.Hour),
	}

	tests := []struct {
		name          string
		current       string
		opts          Options
		wantInstalled bool
		wantTarget    string
	}{
		{name: "prerelease within cooldown", current: "v1.0.0", opts: Options{Version: "1.0.0-rc.1"}, wantInstalled: true, wantTarget: "v1.0.0-rc.1"},
		{name: "downgrade", current: "v1.1.0", opts: Options{Version: "v1.0.5"}, wantInstalled: true, wantTarget: "v1.0.5"},
		{name: "cooldown bypassed", current: "v1.0.0", opts: Options{Version: "v1.1.0"}, wantInstalled: true, wantTarget: "v1.1.0"},
		{name: "dev build may install explicit", current: "dev", opts: Options{Version: "v1.0.5"}, wantInstalled: true, wantTarget: "v1.0.5"},
		{name: "equal version is skipped", current: "v1.0.5", opts: Options{Version: "v1.0.5"}, wantTarget: "v1.0.5"},
		{name: "force reinstalls equal", current: "v1.0.5", opts: Options{Version: "v1.0.5", Force: true}, wantInstalled: true, wantTarget: "v1.0.5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newSUFixture(t, &fakeAPI{list: list})

			rep, err := selfUpdate(context.Background(), tt.current, tt.opts, f.deps)
			if err != nil {
				t.Fatalf("selfUpdate() error = %v", err)
			}
			if rep.Installed != tt.wantInstalled || rep.Target != tt.wantTarget {
				t.Errorf("report = %+v, want installed=%v target=%q", rep, tt.wantInstalled, tt.wantTarget)
			}
			if f.api.listHits.Load() != 0 {
				t.Error("explicit version listed releases")
			}
		})
	}
}

func TestSelfUpdateRecheckFailsClosed(t *testing.T) {
	t.Parallel()

	stale := []githubRelease{suRelease("v1.1.0", 48*time.Hour)}

	tests := []struct {
		name    string
		exact   githubRelease
		opts    Options
		wantErr string
	}{
		{name: "became draft", exact: suRelease("v1.1.0", 48*time.Hour, func(r *githubRelease) { r.Draft = true }), wantErr: "draft"},
		{name: "became prerelease", exact: suRelease("v1.1.0", 48*time.Hour, func(r *githubRelease) { r.Prerelease = true }), wantErr: "pre-release"},
		{name: "published in the future", exact: suRelease("v1.1.0", -time.Hour), wantErr: "future"},
		{name: "cooldown not elapsed", exact: suRelease("v1.1.0", 2*time.Hour), wantErr: "cooldown"},
		{name: "no matching asset", exact: githubRelease{TagName: "v1.1.0", PublishedAt: suNow.Add(-48 * time.Hour)}, wantErr: "asset"},
		{name: "duplicate asset", exact: suRelease("v1.1.0", 48*time.Hour, func(r *githubRelease) { r.Assets = append(r.Assets, r.Assets[0]) }), wantErr: "asset"},
		{name: "explicit draft", exact: suRelease("v1.1.0", 48*time.Hour, func(r *githubRelease) { r.Draft = true }), opts: Options{Version: "v1.1.0"}, wantErr: "draft"},
		{name: "explicit future", exact: suRelease("v1.1.0", -time.Hour), opts: Options{Version: "v1.1.0"}, wantErr: "future"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newSUFixture(t, &fakeAPI{list: stale, tags: map[string]githubRelease{"v1.1.0": tt.exact}})

			_, err := selfUpdate(context.Background(), "v1.0.0", tt.opts, f.deps)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("selfUpdate() error = %v, want containing %q", err, tt.wantErr)
			}
			if f.fetched.Load() != 0 {
				t.Error("downloaded an archive for an ineligible release")
			}
			f.assertUntouched(t)
		})
	}

	t.Run("exact metadata unavailable", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: stale, tags: map[string]githubRelease{}})
		f.deps.releaseURL += "/missing"

		if _, err := selfUpdate(context.Background(), "v1.0.0", Options{Version: "v9.9.9"}, f.deps); err == nil {
			t.Fatal("selfUpdate() succeeded without release metadata")
		}
		f.assertUntouched(t)
	})
}

func TestSelfUpdateUsesCachedCandidates(t *testing.T) {
	t.Parallel()

	f := newSUFixture(t, &fakeAPI{list: nil})
	writeState(t, f.state, state{
		CheckedAt:  suNow.Add(-time.Hour),
		Candidates: []releaseCandidate{{Tag: "v1.1.0", PublishedAt: suNow.Add(-48 * time.Hour)}},
	})
	f.api.tags = map[string]githubRelease{"v1.1.0": suRelease("v1.1.0", 48*time.Hour)}

	rep, err := selfUpdate(context.Background(), "v1.0.0", Options{Check: true}, f.deps)
	if err != nil {
		t.Fatalf("selfUpdate() error = %v", err)
	}
	if rep.Target != "v1.1.0" || f.api.listHits.Load() != 0 {
		t.Errorf("report = %+v, list hits %d; want cached candidate without listing", rep, f.api.listHits.Load())
	}
}

func TestSelfUpdateInstallFailuresLeaveExecutable(t *testing.T) {
	t.Parallel()

	t.Run("archive fetch fails", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		f.deps.fetchArchive = func(context.Context, githubRelease, githubAsset, string) (*os.File, verifyResult, error) {
			return nil, verifyResult{}, errors.New("digest mismatch")
		}

		_, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		f.assertUntouched(t)
	})

	t.Run("archive without jailoc", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		f.deps.fetchArchive = func(_ context.Context, _ githubRelease, _ githubAsset, dir string) (*os.File, verifyResult, error) {
			file, err := os.CreateTemp(dir, "archive-*.tar.gz")
			if err != nil {
				return nil, verifyResult{}, err
			}
			_, err = file.Write(tarGzBytes(t, []tarEntry{fileEntry("README.md", "x")}))

			return file, verifyResult{}, err
		}

		if _, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps); err == nil {
			t.Fatal("selfUpdate() succeeded")
		}
		f.assertUntouched(t)
	})

	t.Run("replace fails", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		f.deps.replace = func(context.Context, string, string) error { return errors.New("rename denied") }
		if err := os.WriteFile(f.state, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err == nil || rep.Installed {
			t.Fatalf("selfUpdate() = %+v, %v; want failure", rep, err)
		}
		if _, statErr := os.Stat(f.state); statErr != nil {
			t.Errorf("state removed although nothing was installed: %v", statErr)
		}
		f.assertUntouched(t)
	})
}

func TestSelfUpdateStaleExecutableKeepsNewerReplacement(t *testing.T) {
	t.Parallel()

	const newerBinary = "NEWER-BINARY"

	f := newSUFixture(t, &fakeAPI{list: standardList()})
	fetch := f.deps.fetchArchive
	f.deps.fetchArchive = func(ctx context.Context, rel githubRelease, asset githubAsset, dir string) (*os.File, verifyResult, error) {
		file, verified, err := fetch(ctx, rel, asset, dir)
		if err != nil {
			return nil, verifyResult{}, err
		}

		// Another updater finishes while this one is still downloading.
		other := filepath.Join(filepath.Dir(f.exe), ".other-updater")
		if err := os.WriteFile(other, []byte(newerBinary), 0o755); err != nil { //nolint:gosec // the test executable must be executable
			t.Fatal(err)
		}
		if err := os.Rename(other, f.exe); err != nil {
			t.Fatal(err)
		}

		return file, verified, nil
	}
	if err := os.WriteFile(f.state, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
	if !errors.Is(err, errExecutableChanged) || !strings.Contains(err.Error(), "run the update again") || rep.Installed {
		t.Fatalf("selfUpdate() = %+v, %v; want a retryable stale executable error", rep, err)
	}
	if got := f.binary(t); got != newerBinary {
		t.Errorf("executable = %q, want the newer replacement %q preserved", got, newerBinary)
	}
	if _, statErr := os.Stat(f.state); statErr != nil {
		t.Errorf("state removed although nothing was installed: %v", statErr)
	}
	f.assertNoTemps(t)
}

func TestSelfUpdateInstalledWithWarnings(t *testing.T) {
	t.Parallel()

	t.Run("directory sync failure", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		f.deps.replace = func(ctx context.Context, target, tmp string) error {
			return replaceExecutableWith(ctx, target, tmp, func(string) error { return errors.New("sync boom") })
		}

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if !rep.Installed || !strings.Contains(rep.Warning, "sync") {
			t.Errorf("report = %+v, want installed with sync warning", rep)
		}
		if got := f.binary(t); got != suNewBinary {
			t.Errorf("executable = %q", got)
		}
	})

	t.Run("state invalidation failure", func(t *testing.T) {
		t.Parallel()

		f := newSUFixture(t, &fakeAPI{list: standardList()})
		if err := os.MkdirAll(filepath.Join(f.state, "child"), 0o750); err != nil {
			t.Fatal(err)
		}

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if !rep.Installed || rep.Warning == "" {
			t.Errorf("report = %+v, want installed with warning", rep)
		}
	})
}
