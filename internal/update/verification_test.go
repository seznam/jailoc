package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testArchiveName = "jailoc_1.2.3_linux_amd64.tar.gz"
	testHexA        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testHexB        = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestParseChecksumManifest(t *testing.T) {
	t.Parallel()

	line := func(hexSum, name string) string { return hexSum + "  " + name + "\n" }

	tests := []struct {
		name    string
		data    string
		want    string
		wantErr string
	}{
		{name: "single entry", data: line(testHexA, testArchiveName), want: testHexA},
		{
			name: "entry among others",
			data: line(testHexB, "jailoc_1.2.3_darwin_arm64.tar.gz") + line(testHexA, testArchiveName) +
				line(testHexB, testArchiveName+".sbom.json") + line(testHexB, "x"+testArchiveName),
			want: testHexA,
		},
		{name: "missing filename", data: line(testHexB, "other.tar.gz"), wantErr: "no checksum"},
		{name: "empty manifest", data: "", wantErr: "no checksum"},
		{name: "duplicate filename", data: line(testHexA, testArchiveName) + line(testHexA, testArchiveName), wantErr: "duplicate"},
		{name: "duplicate with different digest", data: line(testHexA, testArchiveName) + line(testHexB, testArchiveName), wantErr: "duplicate"},
		{name: "short digest", data: line(testHexA[:63], testArchiveName), wantErr: "malformed"},
		{name: "long digest", data: line(testHexA+"a", testArchiveName), wantErr: "malformed"},
		{name: "uppercase digest", data: line(strings.ToUpper(testHexA), testArchiveName), wantErr: "malformed"},
		{name: "non hex digest", data: line(strings.Repeat("g", 64), testArchiveName), wantErr: "malformed"},
		{name: "single space separator", data: testHexA + " " + testArchiveName + "\n", wantErr: "malformed"},
		{name: "binary mode marker", data: testHexA + " *" + testArchiveName + "\n", wantErr: "malformed"},
		{name: "missing filename column", data: testHexA + "  \n", wantErr: "malformed"},
		{name: "malformed unrelated line", data: line(testHexA, testArchiveName) + "garbage\n", wantErr: "malformed"},
		{name: "oversized manifest", data: line(testHexA, testArchiveName) + strings.Repeat("#", maxChecksumManifestSize), wantErr: "exceeds"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseChecksumManifest([]byte(tt.data), testArchiveName)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseChecksumManifest() error = %v, want containing %q", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("parseChecksumManifest() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("parseChecksumManifest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAssetSHA256(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		asset   githubAsset
		want    string
		wantErr string
	}{
		{name: "valid", asset: githubAsset{Digest: "sha256:" + testHexA, Size: 1024}, want: testHexA},
		{name: "size at cap", asset: githubAsset{Digest: "sha256:" + testHexA, Size: maxBinarySize}, want: testHexA},
		{name: "missing digest", asset: githubAsset{Size: 1024}, wantErr: "digest"},
		{name: "other algorithm", asset: githubAsset{Digest: "sha512:" + testHexA, Size: 1024}, wantErr: "digest"},
		{name: "no algorithm prefix", asset: githubAsset{Digest: testHexA, Size: 1024}, wantErr: "digest"},
		{name: "short hex", asset: githubAsset{Digest: "sha256:" + testHexA[:63], Size: 1024}, wantErr: "digest"},
		{name: "uppercase hex", asset: githubAsset{Digest: "sha256:" + strings.ToUpper(testHexA), Size: 1024}, wantErr: "digest"},
		{name: "zero size", asset: githubAsset{Digest: "sha256:" + testHexA}, wantErr: "size"},
		{name: "negative size", asset: githubAsset{Digest: "sha256:" + testHexA, Size: -1}, wantErr: "size"},
		{name: "size above cap", asset: githubAsset{Digest: "sha256:" + testHexA, Size: maxBinarySize + 1}, wantErr: "size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := assetSHA256(tt.asset)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("assetSHA256() error = %v, want containing %q", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("assetSHA256() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("assetSHA256() = %q, want %q", got, tt.want)
			}
		})
	}
}

func sumHex(data []byte) string {
	sum := sha256.Sum256(data)

	return hex.EncodeToString(sum[:])
}

func archiveServer(t *testing.T, status int, body []byte) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func assertEmptyDir(t *testing.T, dir string) {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("directory %s not empty: %v", dir, entries)
	}
}

func TestDownloadArchive(t *testing.T) {
	t.Parallel()

	// A real tar.gz proves the download is stored verbatim, never decompressed.
	body := tarGzBytes(t, []tarEntry{fileEntry("jailoc", "BINARY")})

	t.Run("stores verified bytes verbatim", func(t *testing.T) {
		t.Parallel()

		srv := archiveServer(t, http.StatusOK, body)
		dir := t.TempDir()

		f, err := downloadArchive(context.Background(), srv.Client(), srv.URL, int64(len(body)), sumHex(body), dir)
		if err != nil {
			t.Fatalf("downloadArchive() error = %v", err)
		}
		t.Cleanup(func() { _ = f.Close() })

		got, err := os.ReadFile(f.Name())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, body) {
			t.Errorf("stored %d bytes, want the %d downloaded bytes", len(got), len(body))
		}
		if filepath.Dir(f.Name()) != dir {
			t.Errorf("archive stored in %s, want %s", filepath.Dir(f.Name()), dir)
		}
	})

	failures := []struct {
		name    string
		status  int
		body    []byte
		size    int64
		digest  string
		wantErr string
	}{
		{name: "digest mismatch", status: http.StatusOK, body: body, size: int64(len(body)), digest: testHexA, wantErr: "digest"},
		{name: "body longer than advertised", status: http.StatusOK, body: append(append([]byte(nil), body...), 'x'), size: int64(len(body)), digest: sumHex(body), wantErr: "size"},
		{name: "body shorter than advertised", status: http.StatusOK, body: body[:len(body)-1], size: int64(len(body)), digest: sumHex(body), wantErr: "size"},
		{name: "http error", status: http.StatusNotFound, body: body, size: int64(len(body)), digest: sumHex(body), wantErr: "status"},
		{name: "advertised size above cap", status: http.StatusOK, body: body, size: maxBinarySize + 1, digest: sumHex(body), wantErr: "size"},
		{name: "non positive advertised size", status: http.StatusOK, body: body, size: 0, digest: sumHex(body), wantErr: "size"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := archiveServer(t, tt.status, tt.body)
			dir := t.TempDir()

			f, err := downloadArchive(context.Background(), srv.Client(), srv.URL, tt.size, tt.digest, dir)
			if err == nil {
				_ = f.Close()
				t.Fatal("downloadArchive() succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want containing %q", err, tt.wantErr)
			}
			assertEmptyDir(t, dir)
		})
	}

	t.Run("canceled context", func(t *testing.T) {
		t.Parallel()

		srv := archiveServer(t, http.StatusOK, body)
		dir := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		f, err := downloadArchive(ctx, srv.Client(), srv.URL, int64(len(body)), sumHex(body), dir)
		if err == nil {
			_ = f.Close()
			t.Fatal("downloadArchive() succeeded with canceled context")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		assertEmptyDir(t, dir)
	})
}

var testWorkflow = workflowIdentity{
	Repository: "https://github.com/seznam/jailoc",
	Path:       ".github/workflows/release-please.yml",
	Ref:        "refs/tags/v1.2.3",
}

type attestationSpec struct {
	name, digest string
	workflow     workflowIdentity
	predicate    string
}

func goodAttestation() attestationSpec {
	return attestationSpec{
		name: testArchiveName, digest: testHexA, workflow: testWorkflow,
		predicate: "https://slsa.dev/provenance/v1",
	}
}

func attestationEntry(t *testing.T, s attestationSpec) map[string]any {
	t.Helper()

	statement := map[string]any{
		"_type": "https://in-toto.io/Statement/v1",
		"subject": []any{map[string]any{
			"name": s.name, "digest": map[string]string{"sha256": s.digest},
		}},
		"predicateType": s.predicate,
		"predicate": map[string]any{"buildDefinition": map[string]any{"externalParameters": map[string]any{
			"workflow": map[string]string{"repository": s.workflow.Repository, "path": s.workflow.Path, "ref": s.workflow.Ref},
		}}},
	}
	raw, err := json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}

	return map[string]any{"bundle": map[string]any{"dsseEnvelope": map[string]any{
		"payloadType": "application/vnd.in-toto+json",
		"payload":     base64.StdEncoding.EncodeToString(raw),
	}}}
}

func attestationBody(t *testing.T, specs ...attestationSpec) []byte {
	t.Helper()

	entries := make([]any, 0, len(specs))
	for _, s := range specs {
		entries = append(entries, attestationEntry(t, s))
	}
	data, err := json.Marshal(map[string]any{"attestations": entries})
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestCheckAttestations(t *testing.T) {
	t.Parallel()

	mutate := func(f func(*attestationSpec)) attestationSpec {
		s := goodAttestation()
		f(&s)

		return s
	}

	tests := []struct {
		name    string
		body    func(t *testing.T) []byte
		wantErr string
	}{
		{name: "matching attestation", body: func(t *testing.T) []byte { return attestationBody(t, goodAttestation()) }},
		{
			name: "matching among non matching",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.digest = testHexB }), goodAttestation())
			},
		},
		{name: "no attestations", body: func(t *testing.T) []byte { return attestationBody(t) }, wantErr: "attestation"},
		{
			name: "wrong subject name",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.name = "other.tar.gz" }))
			},
			wantErr: "attestation",
		},
		{
			name: "wrong subject digest",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.digest = testHexB }))
			},
			wantErr: "attestation",
		},
		{
			name: "wrong workflow repository",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.workflow.Repository = "https://github.com/evil/jailoc" }))
			},
			wantErr: "attestation",
		},
		{
			name: "wrong workflow path",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.workflow.Path = ".github/workflows/evil.yml" }))
			},
			wantErr: "attestation",
		},
		{
			name: "wrong workflow ref",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.workflow.Ref = "refs/heads/main" }))
			},
			wantErr: "attestation",
		},
		{
			name: "not SLSA provenance",
			body: func(t *testing.T) []byte {
				return attestationBody(t, mutate(func(s *attestationSpec) { s.predicate = "https://example.com/other" }))
			},
			wantErr: "attestation",
		},
		{name: "malformed json", body: func(*testing.T) []byte { return []byte("{") }, wantErr: "decode"},
		{
			name: "malformed payload",
			body: func(*testing.T) []byte {
				return []byte(`{"attestations":[{"bundle":{"dsseEnvelope":{"payload":"!!!"}}}]}`)
			},
			wantErr: "attestation",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkAttestations(tt.body(t), testArchiveName, testHexA, testWorkflow)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkAttestations() error = %v", err)
				}

				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("checkAttestations() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestMatchAttestations(t *testing.T) {
	t.Parallel()

	const tag = "v1.2.3"

	identity := func(path, ref string) workflowIdentity {
		return workflowIdentity{Repository: "https://github.com/seznam/jailoc", Path: path, Ref: ref}
	}
	body := func(t *testing.T, w workflowIdentity) []byte {
		t.Helper()

		s := goodAttestation()
		s.workflow = w

		return attestationBody(t, s)
	}

	for _, path := range []string{".github/workflows/release-please.yml", ".github/workflows/release.yml", ".github/workflows/prerelease.yml"} {
		for _, ref := range []string{"refs/heads/main", "refs/tags/" + tag} {
			t.Run("accepts "+path+"@"+ref, func(t *testing.T) {
				t.Parallel()

				if err := matchAttestations(body(t, identity(path, ref)), testArchiveName, testHexA, tag); err != nil {
					t.Fatalf("matchAttestations() error = %v", err)
				}
			})
		}
	}

	rejected := []struct {
		name string
		w    workflowIdentity
	}{
		{"other tag", identity(".github/workflows/release.yml", "refs/tags/v9.9.9")},
		{"other branch", identity(".github/workflows/release.yml", "refs/heads/dev")},
		{"other workflow", identity(".github/workflows/ci.yml", "refs/heads/main")},
		{"other repository", workflowIdentity{Repository: "https://github.com/evil/jailoc", Path: ".github/workflows/release.yml", Ref: "refs/heads/main"}},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()

			err := matchAttestations(body(t, tt.w), testArchiveName, testHexA, tag)
			if err == nil || !strings.Contains(err.Error(), "attestation") {
				t.Fatalf("matchAttestations() error = %v, want attestation mismatch", err)
			}
		})
	}

	t.Run("malformed json is a decode error", func(t *testing.T) {
		t.Parallel()

		if err := matchAttestations([]byte("{"), testArchiveName, testHexA, tag); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("matchAttestations() error = %v, want decode error", err)
		}
	})
}

func TestAttestationURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name, releaseURL, want, wantErr string
	}{
		{name: "production", releaseURL: DefaultReleaseURL, want: "https://api.github.com/repos/seznam/jailoc/attestations/sha256:" + testHexA + "?per_page=100"},
		{name: "trailing slash", releaseURL: "http://127.0.0.1:1/releases/", want: "http://127.0.0.1:1/attestations/sha256:" + testHexA + "?per_page=100"},
		{name: "query dropped", releaseURL: "http://127.0.0.1:1/r/releases?x=1", want: "http://127.0.0.1:1/r/attestations/sha256:" + testHexA + "?per_page=100"},
		{name: "not a releases url", releaseURL: "http://127.0.0.1:1/tags", wantErr: "release URL"},
		{name: "unparsable", releaseURL: "http://[::1", wantErr: "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := attestationURL(tt.releaseURL, testHexA)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("attestationURL() error = %v, want containing %q", err, tt.wantErr)
				}

				return
			}
			if err != nil || got != tt.want {
				t.Errorf("attestationURL() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestFetchAttestations(t *testing.T) {
	t.Parallel()

	serve := func(t *testing.T, status int, body []byte) (string, *http.Request) {
		t.Helper()

		var seen http.Request
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = *r.Clone(context.Background())
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		return srv.URL + "/releases", &seen
	}

	t.Run("reads the attestation endpoint", func(t *testing.T) {
		t.Parallel()

		body := attestationBody(t, goodAttestation())
		releaseURL, seen := serve(t, http.StatusOK, body)

		got, err := fetchAttestations(context.Background(), http.DefaultClient, releaseURL, testHexA)
		if err != nil || !bytes.Equal(got, body) {
			t.Fatalf("fetchAttestations() = %d bytes, %v; want the served body", len(got), err)
		}
		if seen.URL.Path != "/attestations/sha256:"+testHexA {
			t.Errorf("requested %q", seen.URL.Path)
		}
		if seen.Header.Get("Authorization") != "" {
			t.Error("attestation request carried credentials")
		}
	})

	t.Run("exactly 4MiB is accepted", func(t *testing.T) {
		t.Parallel()

		releaseURL, _ := serve(t, http.StatusOK, bytes.Repeat([]byte(" "), maxAttestationMetadataSize))

		if _, err := fetchAttestations(context.Background(), http.DefaultClient, releaseURL, testHexA); err != nil {
			t.Fatalf("fetchAttestations() error = %v", err)
		}
	})

	t.Run("above 4MiB is rejected", func(t *testing.T) {
		t.Parallel()

		releaseURL, _ := serve(t, http.StatusOK, bytes.Repeat([]byte(" "), maxAttestationMetadataSize+1))

		if _, err := fetchAttestations(context.Background(), http.DefaultClient, releaseURL, testHexA); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("fetchAttestations() error = %v, want size limit error", err)
		}
	})

	t.Run("http error", func(t *testing.T) {
		t.Parallel()

		releaseURL, _ := serve(t, http.StatusNotFound, []byte("{}"))

		if _, err := fetchAttestations(context.Background(), http.DefaultClient, releaseURL, testHexA); err == nil || !strings.Contains(err.Error(), "status 404") {
			t.Fatalf("fetchAttestations() error = %v, want status error", err)
		}
	})
}

func TestCertIdentityRegex(t *testing.T) {
	t.Parallel()

	re, err := regexp.Compile(certIdentityRegex("v1.2.3"))
	if err != nil {
		t.Fatalf("regex does not compile: %v", err)
	}

	const prefix = "https://github.com/seznam/jailoc/.github/workflows/"

	for _, workflow := range []string{"release-please.yml", "release.yml", "prerelease.yml"} {
		for _, ref := range []string{"refs/heads/main", "refs/tags/v1.2.3"} {
			if id := prefix + workflow + "@" + ref; !re.MatchString(id) {
				t.Errorf("regex rejects %q", id)
			}
		}
	}

	rejected := []string{
		prefix + "ci.yml@refs/heads/main",
		prefix + "release.yml@refs/tags/v1.2.4",
		prefix + "release.yml@refs/tags/v1x2y3",
		prefix + "release.yml@refs/tags/v1.2.3-rc.1",
		prefix + "release.yml@refs/heads/main-evil",
		prefix + "release.yml@refs/heads/dev",
		"x" + prefix + "release.yml@refs/heads/main",
		"https://github.com/evil/jailoc/.github/workflows/release.yml@refs/heads/main",
		prefix + "release.yml@refs/heads/main\n",
		prefix + "releaseXyml@refs/heads/main",
	}
	for _, id := range rejected {
		if re.MatchString(id) {
			t.Errorf("regex accepts %q", id)
		}
	}
}

func TestGHVerifyArgs(t *testing.T) {
	t.Parallel()

	got := ghVerifyArgs("/tmp/x/archive.tar.gz", "v1.2.3")
	want := []string{
		"attestation", "verify", "/tmp/x/archive.tar.gz",
		"--repo", "seznam/jailoc",
		"--hostname", "github.com",
		"--cert-identity-regex", certIdentityRegex("v1.2.3"),
		"--cert-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--deny-self-hosted-runners",
	}
	if !slices.Equal(got, want) {
		t.Errorf("ghVerifyArgs() = %q, want %q", got, want)
	}
}

// ghRecorder is a fake gh that records its invocations.
type ghRecorder struct {
	mu      sync.Mutex
	calls   int
	path    string
	args    []string
	archive []byte // content of the archive argument when gh ran
	out     []byte
	err     error
	hook    func(ctx context.Context)
}

func (g *ghRecorder) client() ghClient {
	return ghClient{
		lookPath: func(string) (string, error) { return "/fake/gh", nil },
		run: func(ctx context.Context, path string, args ...string) ([]byte, error) {
			g.mu.Lock()
			defer g.mu.Unlock()

			g.calls++
			g.path = path
			g.args = slices.Clone(args)
			if len(args) > 2 {
				g.archive, _ = os.ReadFile(args[2])
			}
			if g.hook != nil {
				g.hook(ctx)
			}

			return g.out, g.err
		},
	}
}

func (g *ghRecorder) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.calls
}

func TestVerifyProvenance(t *testing.T) {
	t.Parallel()

	const archive = "/work/jailoc.tar.gz"

	absent := func(string) (string, error) { return "", errors.New("not found") }

	t.Run("gh absent warns that the signature was not checked", func(t *testing.T) {
		t.Parallel()

		gh := ghClient{
			lookPath: absent,
			run:      func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("must not run") },
		}

		res, err := verifyProvenance(context.Background(), gh, archive, "v1.2.3")
		if err != nil {
			t.Fatalf("verifyProvenance() error = %v", err)
		}
		if res.Signature || !strings.Contains(res.Warning, "gh") || !strings.Contains(res.Warning, "signature") || !strings.Contains(res.Warning, "TLS") {
			t.Errorf("result = %+v, want a warning naming gh, the unchecked signature and TLS reliance", res)
		}
	})

	t.Run("zero value client counts as absent", func(t *testing.T) {
		t.Parallel()

		res, err := verifyProvenance(context.Background(), ghClient{}, archive, "v1.2.3")
		if err != nil || res.Signature || res.Warning == "" {
			t.Errorf("verifyProvenance() = %+v, %v; want warning", res, err)
		}
	})

	t.Run("gh success is the signature", func(t *testing.T) {
		t.Parallel()

		// gh output is informational and is never parsed.
		rec := &ghRecorder{out: []byte(`{"verified":false} garbage`)}

		res, err := verifyProvenance(context.Background(), rec.client(), archive, "v1.2.3")
		if err != nil {
			t.Fatalf("verifyProvenance() error = %v", err)
		}
		if !res.Signature || res.Warning != "" {
			t.Errorf("result = %+v, want signature without warning", res)
		}
		if rec.count() != 1 || rec.path != "/fake/gh" || !slices.Equal(rec.args, ghVerifyArgs(archive, "v1.2.3")) {
			t.Errorf("gh ran %d times as %s %q, want once with the verify argv", rec.count(), rec.path, rec.args)
		}
	})

	t.Run("nonzero exit is fatal with bounded detail", func(t *testing.T) {
		t.Parallel()

		rec := &ghRecorder{out: append([]byte("verification failed: bad certificate\n"), bytes.Repeat([]byte("x"), 1<<20)...), err: errors.New("exit status 1")}

		_, err := verifyProvenance(context.Background(), rec.client(), archive, "v1.2.3")
		if err == nil {
			t.Fatal("verifyProvenance() succeeded, want error")
		}
		for _, want := range []string{"gh attestation verify", "exit status 1", "bad certificate"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %.200q, want containing %q", err, want)
			}
		}
		if len(err.Error()) > 4<<10 {
			t.Errorf("error is %d bytes, want bounded detail", len(err.Error()))
		}
	})

	t.Run("canceled before running", func(t *testing.T) {
		t.Parallel()

		rec := &ghRecorder{}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := verifyProvenance(ctx, rec.client(), archive, "v1.2.3")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("verifyProvenance() error = %v, want context.Canceled", err)
		}
		if rec.count() != 0 {
			t.Errorf("gh ran %d times after cancellation", rec.count())
		}
	})

	t.Run("canceled while running", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rec := &ghRecorder{err: errors.New("signal: killed"), hook: func(context.Context) { cancel() }}

		_, err := verifyProvenance(ctx, rec.client(), archive, "v1.2.3")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("verifyProvenance() error = %v, want context.Canceled", err)
		}
	})
}

func writeScript(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil { //nolint:gosec // the fake gh must be executable
		t.Fatal(err)
	}

	return path
}

func TestRunGH(t *testing.T) {
	t.Parallel()

	t.Run("passes argv without a shell and combines output", func(t *testing.T) {
		t.Parallel()

		script := writeScript(t, `echo "out:$1|$2"; echo "err:$3" >&2; exit 3`)

		out, err := runGH(context.Background(), script, "a b", "$(touch pwned)", ";x")
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 3 {
			t.Fatalf("runGH() error = %v, want exit status 3", err)
		}
		for _, want := range []string{"out:a b|$(touch pwned)", "err:;x"} {
			if !strings.Contains(string(out), want) {
				t.Errorf("output %q lacks %q", out, want)
			}
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		if _, err := runGH(context.Background(), writeScript(t, "exit 0")); err != nil {
			t.Fatalf("runGH() error = %v", err)
		}
	})

	t.Run("output is capped", func(t *testing.T) {
		t.Parallel()

		out, err := runGH(context.Background(), writeScript(t, "head -c 1000000 /dev/zero | tr '\\0' x"))
		if err != nil {
			t.Fatalf("runGH() error = %v", err)
		}
		if len(out) == 0 || len(out) > maxGHOutput {
			t.Errorf("captured %d bytes, want 1..%d", len(out), maxGHOutput)
		}
	})

	t.Run("does not prompt or page", func(t *testing.T) {
		t.Parallel()

		out, err := runGH(context.Background(), writeScript(t, `echo "$GH_PROMPT_DISABLED $GH_NO_UPDATE_NOTIFIER"`))
		if err != nil || strings.TrimSpace(string(out)) != "1 1" {
			t.Errorf("runGH() = %q, %v; want prompts and update notifier disabled", out, err)
		}
	})
}

func TestReleaseRedirectPolicy(t *testing.T) {
	t.Parallel()

	redirect := func(t *testing.T, rawURL string, hops int) (*http.Request, []*http.Request) {
		t.Helper()

		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer secret")

		return req, make([]*http.Request, hops)
	}

	for _, host := range []string{"github.com", "api.github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		t.Run("allows "+host, func(t *testing.T) {
			t.Parallel()

			req, via := redirect(t, "https://"+host+"/x", 1)
			if err := releaseRedirectPolicy(req, via); err != nil {
				t.Fatalf("releaseRedirectPolicy() error = %v", err)
			}
			if req.Header.Get("Authorization") != "" {
				t.Error("Authorization survived the redirect")
			}
		})
	}

	rejected := []struct{ name, url string }{
		{"plain http", "http://github.com/x"},
		{"unlisted host", "https://evil.example/x"},
		{"lookalike suffix", "https://github.com.evil.example/x"},
		{"lookalike prefix", "https://evilgithub.com/x"},
		{"other githubusercontent host", "https://raw.githubusercontent.com/x"},
		{"credentials in url", "https://user@github.com/x"},
	}
	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			t.Parallel()

			req, via := redirect(t, tt.url, 1)
			if err := releaseRedirectPolicy(req, via); err == nil {
				t.Fatal("releaseRedirectPolicy() accepted the redirect")
			}
		})
	}

	t.Run("caps the chain at 10 redirects", func(t *testing.T) {
		t.Parallel()

		req, via := redirect(t, "https://github.com/x", 9)
		if err := releaseRedirectPolicy(req, via); err != nil {
			t.Fatalf("10th redirect error = %v", err)
		}

		req, via = redirect(t, "https://github.com/x", 10)
		if err := releaseRedirectPolicy(req, via); err == nil {
			t.Fatal("11th redirect accepted")
		}
	})

	t.Run("production client refuses redirects outside the allow list", func(t *testing.T) {
		t.Parallel()

		// The production policy refuses the loopback test server, which proves it is wired in.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://127.0.0.1:1/x", http.StatusFound)
		}))
		t.Cleanup(srv.Close)

		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := newReleaseClient().Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "redirect") {
			t.Fatalf("Do() error = %v, want a redirect policy error", err)
		}
	})
}

func TestDefaultDeps(t *testing.T) {
	t.Parallel()

	d := defaultDeps()

	if d.releaseURL != DefaultReleaseURL || d.fetchArchive != nil {
		t.Errorf("releaseURL = %q, fetchArchive set = %v", d.releaseURL, d.fetchArchive != nil)
	}
	if d.client == nil || d.client.Timeout != downloadTimeout || d.client.CheckRedirect == nil {
		t.Errorf("client = %+v, want the download timeout and the redirect policy", d.client)
	}
	if d.gh.lookPath == nil || d.gh.run == nil {
		t.Error("gh client is not configured")
	}
}

// fakeGitHub serves the release API, assets, checksums and attestations of one release.
type fakeGitHub struct {
	tb      *testing.T
	srv     *httptest.Server
	tag     string
	archive []byte

	// Overrides for failure scenarios; zero values serve the genuine release.
	checksum       string // checksums.txt digest of the archive
	digest         string // digest advertised by the release API
	served         []byte // bytes served for the archive
	attestations   []attestationSpec
	attestStatus   int
	noChecksums    bool
	onDownload     func(w http.ResponseWriter, r *http.Request)
	downloads      atomic.Int32
	checksumHits   atomic.Int32
	attestHits     atomic.Int32
	attestRequests []string
	mu             sync.Mutex
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()

	g := &fakeGitHub{tb: t, tag: "v1.1.0", archive: tarGzBytes(t, []tarEntry{fileEntry("jailoc", suNewBinary)})}
	g.srv = httptest.NewServer(g)
	t.Cleanup(g.srv.Close)

	return g
}

func (g *fakeGitHub) name() string { return archiveAssetName(g.tag) }

func (g *fakeGitHub) sum() string { return sumHex(g.archive) }

func (g *fakeGitHub) release() githubRelease {
	digest := g.digest
	if digest == "" {
		digest = g.sum()
	}

	rel := githubRelease{TagName: g.tag, PublishedAt: suNow.Add(-48 * time.Hour), Assets: []githubAsset{{
		Name: g.name(), Size: int64(len(g.archive)), Digest: "sha256:" + digest, BrowserDownloadURL: g.srv.URL + "/download/" + g.name(),
	}}}
	if !g.noChecksums {
		rel.Assets = append(rel.Assets, githubAsset{Name: checksumsAssetName, Size: 100, BrowserDownloadURL: g.srv.URL + "/download/" + checksumsAssetName})
	}

	return rel
}

func (g *fakeGitHub) genuineAttestation() attestationSpec {
	return attestationSpec{
		name: g.name(), digest: g.sum(), predicate: "https://slsa.dev/provenance/v1",
		workflow: workflowIdentity{Repository: "https://github.com/seznam/jailoc", Path: ".github/workflows/release-please.yml", Ref: "refs/heads/main"},
	}
}

func (g *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/releases":
		_ = json.NewEncoder(w).Encode([]githubRelease{g.release()})
	case r.URL.Path == "/releases/tags/"+g.tag:
		_ = json.NewEncoder(w).Encode(g.release())
	case r.URL.Path == "/download/"+g.name():
		g.downloads.Add(1)
		if g.onDownload != nil {
			g.onDownload(w, r)

			return
		}
		body := g.archive
		if g.served != nil {
			body = g.served
		}
		_, _ = w.Write(body)
	case r.URL.Path == "/download/"+checksumsAssetName:
		g.checksumHits.Add(1)
		sum := g.checksum
		if sum == "" {
			sum = g.sum()
		}
		_, _ = w.Write([]byte(sum + "  " + g.name() + "\n" + testHexB + "  jailoc_other.tar.gz\n"))
	case strings.HasPrefix(r.URL.Path, "/attestations/"):
		g.attestHits.Add(1)
		g.mu.Lock()
		g.attestRequests = append(g.attestRequests, r.URL.Path)
		g.mu.Unlock()

		if g.attestStatus != 0 {
			w.WriteHeader(g.attestStatus)

			return
		}
		if r.URL.Path != "/attestations/sha256:"+g.sum() {
			http.NotFound(w, r)

			return
		}
		specs := g.attestations
		if specs == nil {
			specs = []attestationSpec{g.genuineAttestation()}
		}
		_, _ = w.Write(attestationBody(g.tb, specs...))
	default:
		http.NotFound(w, r)
	}
}

func (g *fakeGitHub) fixture(t *testing.T, gh ghClient) *suFixture {
	t.Helper()

	f := newSUFixture(t, &fakeAPI{})
	f.deps.releaseURL = g.srv.URL + "/releases"
	f.deps.fetchArchive = nil
	f.deps.client = g.srv.Client()
	f.deps.gh = gh

	return f
}

func TestSelfUpdateFullVerificationFlow(t *testing.T) {
	t.Parallel()

	t.Run("gh verified install", func(t *testing.T) {
		t.Parallel()

		g := newFakeGitHub(t)
		rec := &ghRecorder{out: []byte("Loaded digest sha256:... Verification succeeded!")}
		f := g.fixture(t, rec.client())

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if !rep.Installed || !rep.VerifiedSignature || rep.Warning != "" || rep.Target != "v1.1.0" {
			t.Errorf("report = %+v, want a signature-verified install", rep)
		}
		if got := f.binary(t); got != suNewBinary {
			t.Errorf("executable = %q, want %q", got, suNewBinary)
		}
		if rec.count() != 1 || len(rec.args) < 3 || !filepath.IsAbs(rec.args[2]) {
			t.Fatalf("gh ran %d times with %q, want once on an absolute archive path", rec.count(), rec.args)
		}
		if !slices.Equal(rec.args, ghVerifyArgs(rec.args[2], "v1.1.0")) {
			t.Errorf("gh argv = %q", rec.args)
		}
		if !bytes.Equal(rec.archive, g.archive) {
			t.Error("gh did not see the verified archive bytes")
		}
		if len(g.attestRequests) != 1 || g.attestRequests[0] != "/attestations/sha256:"+g.sum() {
			t.Errorf("attestation requests = %q", g.attestRequests)
		}
		f.assertNoTemps(t)
	})

	t.Run("tag ref workflow is accepted", func(t *testing.T) {
		t.Parallel()

		g := newFakeGitHub(t)
		spec := g.genuineAttestation()
		spec.workflow.Path = ".github/workflows/release.yml"
		spec.workflow.Ref = "refs/tags/v1.1.0"
		g.attestations = []attestationSpec{spec}
		f := g.fixture(t, (&ghRecorder{}).client())

		if rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps); err != nil || !rep.Installed {
			t.Fatalf("selfUpdate() = %+v, %v; want installed", rep, err)
		}
	})

	t.Run("missing gh installs with an explicit warning", func(t *testing.T) {
		t.Parallel()

		g := newFakeGitHub(t)
		f := g.fixture(t, ghClient{lookPath: func(string) (string, error) { return "", errors.New("not found") }})

		rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
		if err != nil {
			t.Fatalf("selfUpdate() error = %v", err)
		}
		if !rep.Installed || rep.VerifiedSignature || !strings.Contains(rep.Warning, "signature") {
			t.Errorf("report = %+v, want installed, unsigned, with a warning", rep)
		}
		if g.attestHits.Load() != 1 {
			t.Errorf("attestation API hit %d times, want 1 even without gh", g.attestHits.Load())
		}
		if got := f.binary(t); got != suNewBinary {
			t.Errorf("executable = %q, want %q", got, suNewBinary)
		}
	})

	failures := []struct {
		name          string
		setup         func(g *fakeGitHub, rec *ghRecorder)
		wantErr       string
		wantDownloads int32
		wantGHCalls   int
	}{
		{
			name:    "checksums.txt disagrees with the release digest",
			setup:   func(g *fakeGitHub, _ *ghRecorder) { g.checksum = testHexB },
			wantErr: "checksums.txt",
		},
		{
			name:    "release has no checksums.txt",
			setup:   func(g *fakeGitHub, _ *ghRecorder) { g.noChecksums = true },
			wantErr: "asset",
		},
		{
			name: "release digest and checksums.txt agree but the download differs",
			setup: func(g *fakeGitHub, _ *ghRecorder) {
				g.served = bytes.Repeat([]byte("x"), len(g.archive))
			},
			wantErr:       "digest",
			wantDownloads: 1,
		},
		{
			name: "attestation names another digest",
			setup: func(g *fakeGitHub, _ *ghRecorder) {
				s := g.genuineAttestation()
				s.digest = testHexB
				g.attestations = []attestationSpec{s}
			},
			wantErr: "attestation",
		},
		{
			name: "attestation from another workflow",
			setup: func(g *fakeGitHub, _ *ghRecorder) {
				s := g.genuineAttestation()
				s.workflow.Path = ".github/workflows/evil.yml"
				g.attestations = []attestationSpec{s}
			},
			wantErr: "attestation",
		},
		{
			name:    "attestation API error",
			setup:   func(g *fakeGitHub, _ *ghRecorder) { g.attestStatus = http.StatusInternalServerError },
			wantErr: "status 500",
		},
		{
			name: "gh exits nonzero",
			setup: func(_ *fakeGitHub, rec *ghRecorder) {
				rec.err = errors.New("exit status 1")
				rec.out = []byte("no matching attestations")
			},
			wantErr:       "no matching attestations",
			wantDownloads: 1,
			wantGHCalls:   1,
		},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			g := newFakeGitHub(t)
			rec := &ghRecorder{}
			tt.setup(g, rec)
			f := g.fixture(t, rec.client())

			rep, err := selfUpdate(context.Background(), "v1.0.0", Options{}, f.deps)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("selfUpdate() error = %v, want containing %q", err, tt.wantErr)
			}
			if rep.Installed || rep.VerifiedSignature {
				t.Errorf("report = %+v, want nothing installed", rep)
			}
			if g.downloads.Load() != tt.wantDownloads {
				t.Errorf("archive downloaded %d times, want %d", g.downloads.Load(), tt.wantDownloads)
			}
			if rec.count() != tt.wantGHCalls {
				t.Errorf("gh ran %d times, want %d", rec.count(), tt.wantGHCalls)
			}
			f.assertUntouched(t)
		})
	}

	t.Run("cancellation during the download", func(t *testing.T) {
		t.Parallel()

		g := newFakeGitHub(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		g.onDownload = func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(g.archive[:len(g.archive)/2])
			w.(http.Flusher).Flush()
			cancel()
			<-r.Context().Done()
		}
		rec := &ghRecorder{}
		f := g.fixture(t, rec.client())

		_, err := selfUpdate(ctx, "v1.0.0", Options{}, f.deps)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("selfUpdate() error = %v, want context.Canceled", err)
		}
		if rec.count() != 0 {
			t.Errorf("gh ran %d times after cancellation", rec.count())
		}
		f.assertUntouched(t)
	})

	t.Run("cancellation while gh runs", func(t *testing.T) {
		t.Parallel()

		g := newFakeGitHub(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rec := &ghRecorder{err: errors.New("signal: killed"), hook: func(context.Context) { cancel() }}
		f := g.fixture(t, rec.client())

		_, err := selfUpdate(ctx, "v1.0.0", Options{}, f.deps)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("selfUpdate() error = %v, want context.Canceled", err)
		}
		f.assertUntouched(t)
	})
}
