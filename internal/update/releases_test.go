package update

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var testNow = time.Date(2026, time.November, 27, 12, 0, 0, 0, time.UTC)

func cand(tag string, age time.Duration) releaseCandidate {
	return releaseCandidate{Tag: tag, PublishedAt: testNow.Add(-age)}
}

func tagOf(c *releaseCandidate) string {
	if c == nil {
		return ""
	}
	return c.Tag
}

func releaseJSON(tag string, published time.Time) string {
	return fmt.Sprintf(`{"tag_name":%q,"published_at":%q}`, tag, published.UTC().Format(time.RFC3339))
}

func TestNormalizeTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{name: "with v", in: "v1.2.3", want: "v1.2.3"},
		{name: "without v gets prefix", in: "1.2.3", want: "v1.2.3"},
		{name: "prerelease", in: "v1.2.3-rc.1", want: "v1.2.3-rc.1"},
		{name: "zero version", in: "v0.0.0", want: "v0.0.0"},
		{name: "empty", in: "", wantErr: true},
		{name: "only v", in: "v", wantErr: true},
		{name: "two components", in: "v1.2", wantErr: true},
		{name: "four components", in: "v1.2.3.4", wantErr: true},
		{name: "leading space", in: " v1.2.3", wantErr: true},
		{name: "trailing space", in: "v1.2.3 ", wantErr: true},
		{name: "inner space", in: "v1.2.3 -rc", wantErr: true},
		{name: "path separator", in: "v1.2.3/../x", wantErr: true},
		{name: "traversal", in: "../v1.2.3", wantErr: true},
		{name: "backslash", in: `v1.2.3\x`, wantErr: true},
		{name: "query", in: "v1.2.3?x=1", wantErr: true},
		{name: "percent", in: "v1.2.3%2f", wantErr: true},
		{name: "build metadata", in: "v1.2.3+meta", wantErr: true},
		{name: "leading zero", in: "v01.2.3", wantErr: true},
		{name: "empty prerelease", in: "v1.2.3-", wantErr: true},
		{name: "double v", in: "vv1.2.3", wantErr: true},
		{name: "uppercase V", in: "V1.2.3", wantErr: true},
		{name: "latest keyword", in: "latest", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := normalizeTag(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("normalizeTag(%q) = %q, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeTag(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("normalizeTag(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestCandidatesFromReleases(t *testing.T) {
	t.Parallel()

	published := testNow.Add(-48 * time.Hour)
	got := candidatesFromReleases([]githubRelease{
		{TagName: "v1.0.0", PublishedAt: published},
		{TagName: "v1.1.0", PublishedAt: published, Draft: true},
		{TagName: "v1.2.0-rc.1", PublishedAt: published, Prerelease: true},
		{TagName: "v1.3.0"},
		{TagName: "", PublishedAt: published},
		{TagName: "not-a-version", PublishedAt: published},
		{TagName: "v1.4.0", PublishedAt: published, Assets: []githubAsset{{Name: "x"}}},
	})

	want := []releaseCandidate{
		{Tag: "v1.0.0", PublishedAt: published},
		{Tag: "v1.2.0-rc.1", PublishedAt: published, Prerelease: true},
		{Tag: "v1.4.0", PublishedAt: published},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d candidates %+v, want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i].Tag != want[i].Tag || !got[i].PublishedAt.Equal(want[i].PublishedAt) || got[i].Prerelease != want[i].Prerelease {
			t.Fatalf("candidate %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	if got := candidatesFromReleases(nil); len(got) != 0 {
		t.Fatalf("nil input gave %+v", got)
	}
}

func TestSelectRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		candidates   []releaseCandidate
		noCooldown   bool
		wantEligible string
		wantPending  string
	}{
		{name: "empty"},
		{
			name:         "highest semver wins over listing order",
			candidates:   []releaseCandidate{cand("v1.2.5", 25*time.Hour), cand("v2.0.0", 72*time.Hour), cand("v1.9.0", 96*time.Hour)},
			wantEligible: "v2.0.0",
		},
		{
			name:         "backport published later does not beat higher semver",
			candidates:   []releaseCandidate{cand("v1.9.1", 30*time.Hour), cand("v2.0.0", 100*time.Hour)},
			wantEligible: "v2.0.0",
		},
		{
			name:         "exact 24h boundary is eligible",
			candidates:   []releaseCandidate{cand("v1.0.0", 24*time.Hour)},
			wantEligible: "v1.0.0",
		},
		{
			name:        "one nanosecond short of 24h is pending",
			candidates:  []releaseCandidate{cand("v1.0.0", 24*time.Hour-time.Nanosecond)},
			wantPending: "v1.0.0",
		},
		{
			name:         "newer release in cooldown is pending, older eligible stays",
			candidates:   []releaseCandidate{cand("v1.1.0", 2*time.Hour), cand("v1.0.0", 48*time.Hour)},
			wantEligible: "v1.0.0",
			wantPending:  "v1.1.0",
		},
		{
			name:         "pending is highest cooling release",
			candidates:   []releaseCandidate{cand("v1.1.0", 2*time.Hour), cand("v1.2.0", 3*time.Hour), cand("v1.0.0", 48*time.Hour)},
			wantEligible: "v1.0.0",
			wantPending:  "v1.2.0",
		},
		{
			name:         "cooling release lower than eligible is not pending",
			candidates:   []releaseCandidate{cand("v1.0.1", 2*time.Hour), cand("v2.0.0", 48*time.Hour)},
			wantEligible: "v2.0.0",
		},
		{
			name:         "noCooldown selects release inside cooldown",
			candidates:   []releaseCandidate{cand("v1.1.0", time.Minute), cand("v1.0.0", 48*time.Hour)},
			noCooldown:   true,
			wantEligible: "v1.1.0",
		},
		{
			name:       "future publication never eligible",
			candidates: []releaseCandidate{cand("v1.0.0", -time.Minute)},
		},
		{
			name:       "future publication never eligible with noCooldown",
			candidates: []releaseCandidate{cand("v1.0.0", -time.Minute)},
			noCooldown: true,
		},
		{
			name:         "future release does not block older eligible",
			candidates:   []releaseCandidate{cand("v9.0.0", -time.Hour), cand("v1.0.0", 48*time.Hour)},
			noCooldown:   true,
			wantEligible: "v1.0.0",
		},
		{
			name:       "future release is not pending",
			candidates: []releaseCandidate{cand("v9.0.0", -time.Hour)},
		},
		{
			name:         "zero timestamp skipped",
			candidates:   []releaseCandidate{{Tag: "v9.0.0"}, cand("v1.0.0", 48*time.Hour)},
			noCooldown:   true,
			wantEligible: "v1.0.0",
		},
		{
			name:         "prerelease flag skipped",
			candidates:   []releaseCandidate{{Tag: "v2.0.0", PublishedAt: testNow.Add(-72 * time.Hour), Prerelease: true}, cand("v1.0.0", 48*time.Hour)},
			wantEligible: "v1.0.0",
		},
		{
			name:         "prerelease tag skipped",
			candidates:   []releaseCandidate{cand("v2.0.0-rc.1", 72*time.Hour), cand("v1.0.0", 48*time.Hour)},
			wantEligible: "v1.0.0",
		},
		{
			name:         "invalid tags skipped",
			candidates:   []releaseCandidate{cand("latest", 72*time.Hour), cand("../v3.0.0", 72*time.Hour), cand("v1.0.0", 48*time.Hour)},
			wantEligible: "v1.0.0",
		},
		{
			name:         "semver compare is numeric not lexical",
			candidates:   []releaseCandidate{cand("v1.9.0", 72*time.Hour), cand("v1.10.0", 72*time.Hour)},
			wantEligible: "v1.10.0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			eligible, pending := selectRelease(tt.candidates, testNow, tt.noCooldown)
			if got := tagOf(eligible); got != tt.wantEligible {
				t.Fatalf("eligible = %q, want %q", got, tt.wantEligible)
			}
			if got := tagOf(pending); got != tt.wantPending {
				t.Fatalf("pending = %q, want %q", got, tt.wantPending)
			}
		})
	}
}

func TestSelectReleaseDoesNotAliasInput(t *testing.T) {
	t.Parallel()

	in := []releaseCandidate{cand("v1.0.0", 48*time.Hour)}
	eligible, _ := selectRelease(in, testNow, false)
	if eligible == nil {
		t.Fatal("expected eligible")
	}
	eligible.Tag = "mutated"
	if in[0].Tag != "v1.0.0" {
		t.Fatalf("input mutated: %+v", in[0])
	}
}

type listServer struct {
	mu       sync.Mutex
	requests []*http.Request
	srv      *httptest.Server
}

// newListServer serves pages produced by page(n) (1-based) and records requests.
func newListServer(t *testing.T, page func(n int) string) *listServer {
	t.Helper()

	ls := &listServer{}
	ls.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ls.mu.Lock()
		ls.requests = append(ls.requests, r)
		ls.mu.Unlock()

		n, _ := strconv.Atoi(r.URL.Query().Get("page"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(page(n)))
	}))
	t.Cleanup(ls.srv.Close)

	return ls
}

func (ls *listServer) count() int {
	ls.mu.Lock()
	defer ls.mu.Unlock()
	return len(ls.requests)
}

// fullPage returns n releases with tags v<page>.<i>.0.
func fullPage(page, n int) string {
	items := make([]string, 0, n)
	for i := range n {
		items = append(items, releaseJSON(fmt.Sprintf("v%d.%d.0", page, i), testNow.Add(-48*time.Hour)))
	}
	return "[" + strings.Join(items, ",") + "]"
}

func TestFetchReleases(t *testing.T) {
	t.Parallel()

	t.Run("single short page and request shape", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string {
			return `[{"tag_name":"v1.0.0","published_at":"2026-11-01T10:00:00Z","draft":false,"prerelease":true,"immutable":true,` +
				`"assets":[{"name":"jailoc_1.0.0_linux_amd64.tar.gz","size":1234,"digest":"sha256:abc","browser_download_url":"https://example.invalid/a"}]}]`
		})

		got, err := fetchReleases(t.Context(), ls.srv.URL+"/repos/o/r/releases")
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if ls.count() != 1 {
			t.Fatalf("requests = %d, want 1 (short page stops)", ls.count())
		}

		req := ls.requests[0]
		if req.URL.Path != "/repos/o/r/releases" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		q := req.URL.Query()
		if q.Get("per_page") != "10" || q.Get("page") != "1" {
			t.Fatalf("query = %v", q)
		}
		if h := req.Header.Get("Accept"); h != "application/vnd.github+json" {
			t.Fatalf("Accept = %q", h)
		}
		if h := req.Header.Get("User-Agent"); h == "" {
			t.Fatal("missing User-Agent")
		}
		if h := req.Header.Get("X-GitHub-Api-Version"); h == "" {
			t.Fatal("missing X-GitHub-Api-Version")
		}

		if len(got) != 1 {
			t.Fatalf("got %d releases", len(got))
		}
		r := got[0]
		wantTime := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
		if r.TagName != "v1.0.0" || !r.PublishedAt.Equal(wantTime) || r.Draft || !r.Prerelease || !r.Immutable {
			t.Fatalf("release = %+v", r)
		}
		if len(r.Assets) != 1 {
			t.Fatalf("assets = %+v", r.Assets)
		}
		a := r.Assets[0]
		if a.Name != "jailoc_1.0.0_linux_amd64.tar.gz" || a.Size != 1234 || a.Digest != "sha256:abc" || a.BrowserDownloadURL != "https://example.invalid/a" {
			t.Fatalf("asset = %+v", a)
		}
	})

	t.Run("paginates until short page without early stop", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(n int) string {
			if n == 3 {
				return fullPage(n, 3)
			}
			return fullPage(n, 10)
		})

		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if ls.count() != 3 {
			t.Fatalf("requests = %d, want 3", ls.count())
		}
		if len(got) != 23 {
			t.Fatalf("releases = %d, want 23", len(got))
		}
		for i, r := range ls.requests {
			if r.URL.Query().Get("page") != strconv.Itoa(i+1) {
				t.Fatalf("request %d page = %q", i, r.URL.Query().Get("page"))
			}
		}
	})

	t.Run("caps at five pages", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(n int) string { return fullPage(n, 10) })

		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if ls.count() != 5 {
			t.Fatalf("requests = %d, want 5", ls.count())
		}
		if len(got) != 50 {
			t.Fatalf("releases = %d, want 50", len(got))
		}
	})

	t.Run("empty first page", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "[]" })
		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if len(got) != 0 || ls.count() != 1 {
			t.Fatalf("got %d releases, %d requests", len(got), ls.count())
		}
	})

	t.Run("short page counts raw entries so skipped releases do not stop or extend paging", func(t *testing.T) {
		t.Parallel()

		// Page 1 has 10 raw entries of which 2 are malformed: must still continue to page 2.
		ls := newListServer(t, func(n int) string {
			if n == 2 {
				return "[" + releaseJSON("v2.0.0", testNow.Add(-48*time.Hour)) + "]"
			}
			items := []string{`{"tag_name":"v0.0.1","published_at":"garbage"}`, `{"tag_name":"v0.0.2"}`}
			for i := range 8 {
				items = append(items, releaseJSON(fmt.Sprintf("v1.%d.0", i), testNow.Add(-48*time.Hour)))
			}
			return "[" + strings.Join(items, ",") + "]"
		})

		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if ls.count() != 2 {
			t.Fatalf("requests = %d, want 2", ls.count())
		}
		if len(got) != 9 {
			t.Fatalf("releases = %d, want 9", len(got))
		}
	})

	t.Run("malformed publication skips only that release", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string {
			return `[` +
				`{"tag_name":"v1.0.0","published_at":"not a time"},` +
				`{"tag_name":"v1.1.0","published_at":null},` +
				`{"tag_name":"v1.2.0"},` +
				`{"tag_name":"v1.3.0","published_at":12345},` +
				`{"tag_name":"v1.4.0","published_at":"0001-01-01T00:00:00Z"},` +
				`{"tag_name":"","published_at":"2026-11-01T10:00:00Z"},` +
				`"not an object",` +
				`{"tag_name":"v1.5.0","published_at":"2026-11-01T10:00:00Z","assets":"bogus"},` +
				`{"tag_name":"v2.0.0","published_at":"2026-11-02T10:00:00Z"}` +
				`]`
		})

		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if len(got) != 1 || got[0].TagName != "v2.0.0" {
			t.Fatalf("got %+v, want only v2.0.0", got)
		}
	})

	t.Run("preserves existing query parameters", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "[]" })
		if _, err := fetchReleases(t.Context(), ls.srv.URL+"/x?foo=bar&page=9&per_page=100"); err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		q := ls.requests[0].URL.Query()
		if q.Get("foo") != "bar" || q.Get("page") != "1" || q.Get("per_page") != "10" {
			t.Fatalf("query = %v", q)
		}
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(ts.Close)

		_, err := fetchReleases(t.Context(), ts.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "unexpected status 500")
	})

	t.Run("error on later page fails whole fetch", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(fullPage(1, 10)))
		}))
		t.Cleanup(ts.Close)

		_, err := fetchReleases(t.Context(), ts.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "unexpected status 502")
	})

	t.Run("malformed json", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "not json" })
		_, err := fetchReleases(t.Context(), ls.srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "decode release response")
	})

	t.Run("object instead of array", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return `{"tag_name":"v1.0.0"}` })
		_, err := fetchReleases(t.Context(), ls.srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "decode release response")
	})

	t.Run("page larger than 4MiB", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "[" + strings.Repeat(" ", 4<<20) + "]" })
		_, err := fetchReleases(t.Context(), ls.srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "exceeds 4MiB limit")
	})

	t.Run("page of exactly 4MiB is accepted", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "[" + strings.Repeat(" ", 4<<20-2) + "]" })
		got, err := fetchReleases(t.Context(), ls.srv.URL)
		if err != nil {
			t.Fatalf("fetchReleases: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("context cancelled", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "[]" })
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		_, err := fetchReleases(ctx, ls.srv.URL)
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "execute release request")
		if ls.count() != 0 {
			t.Fatalf("requests = %d, want 0", ls.count())
		}
	})

	t.Run("invalid url", func(t *testing.T) {
		t.Parallel()

		if _, err := fetchReleases(t.Context(), "http://[::1"); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestFetchRelease(t *testing.T) {
	t.Parallel()

	t.Run("requests tags endpoint and decodes release", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string {
			return `{"tag_name":"v1.2.3-rc.1","published_at":"2026-11-01T10:00:00Z","prerelease":true,"immutable":true,` +
				`"assets":[{"name":"a","size":1,"digest":"sha256:x","browser_download_url":"https://example.invalid/a"}]}`
		})

		got, err := fetchRelease(t.Context(), ls.srv.URL+"/repos/o/r/releases", "1.2.3-rc.1")
		if err != nil {
			t.Fatalf("fetchRelease: %v", err)
		}
		req := ls.requests[0]
		if req.URL.Path != "/repos/o/r/releases/tags/v1.2.3-rc.1" {
			t.Fatalf("path = %q", req.URL.Path)
		}
		if req.Header.Get("Accept") != "application/vnd.github+json" || req.Header.Get("User-Agent") == "" {
			t.Fatalf("headers = %v", req.Header)
		}
		if got.TagName != "v1.2.3-rc.1" || !got.Prerelease || !got.Immutable || got.PublishedAt.IsZero() || len(got.Assets) != 1 {
			t.Fatalf("release = %+v", got)
		}
	})

	t.Run("invalid tag makes no request", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "{}" })
		for _, tag := range []string{"", "latest", "../x", "v1.2.3/../../x", "v1.2.3 "} {
			if _, err := fetchRelease(t.Context(), ls.srv.URL, tag); err == nil {
				t.Fatalf("tag %q: expected error", tag)
			}
		}
		if ls.count() != 0 {
			t.Fatalf("requests = %d, want 0", ls.count())
		}
	})

	t.Run("not found", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(ts.Close)

		_, err := fetchRelease(t.Context(), ts.URL, "v1.0.0")
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "unexpected status 404")
	})

	t.Run("tag mismatch", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return releaseJSON("v9.9.9", testNow) })
		_, err := fetchRelease(t.Context(), ls.srv.URL, "v1.0.0")
		if err == nil {
			t.Fatal("expected error")
		}
		assertContains(t, err.Error(), "tag mismatch")
	})

	t.Run("missing or malformed publication", func(t *testing.T) {
		t.Parallel()

		for name, body := range map[string]string{
			"missing":   `{"tag_name":"v1.0.0"}`,
			"null":      `{"tag_name":"v1.0.0","published_at":null}`,
			"malformed": `{"tag_name":"v1.0.0","published_at":"yesterday"}`,
		} {
			ls := newListServer(t, func(int) string { return body })
			_, err := fetchRelease(t.Context(), ls.srv.URL, "v1.0.0")
			if err == nil {
				t.Fatalf("%s: expected error", name)
			}
			assertContains(t, err.Error(), "published_at")
		}
	})

	t.Run("malformed json and oversize", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return "nope" })
		if _, err := fetchRelease(t.Context(), ls.srv.URL, "v1.0.0"); err == nil {
			t.Fatal("expected decode error")
		}

		big := newListServer(t, func(int) string { return `{"x":"` + strings.Repeat("a", 4<<20) + `"}` })
		_, err := fetchRelease(t.Context(), big.srv.URL, "v1.0.0")
		if err == nil {
			t.Fatal("expected size error")
		}
		assertContains(t, err.Error(), "exceeds 4MiB limit")
	})

	t.Run("keeps escaped path safe", func(t *testing.T) {
		t.Parallel()

		ls := newListServer(t, func(int) string { return releaseJSON("v1.0.0-a.b", testNow) })
		if _, err := fetchRelease(t.Context(), ls.srv.URL+"/releases", "v1.0.0-a.b"); err != nil {
			t.Fatalf("fetchRelease: %v", err)
		}
		if got := ls.requests[0].URL.EscapedPath(); got != "/releases/tags/"+url.PathEscape("v1.0.0-a.b") {
			t.Fatalf("escaped path = %q", got)
		}
	})
}
