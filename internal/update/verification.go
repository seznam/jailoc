package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// maxChecksumManifestSize bounds checksums.txt.
	maxChecksumManifestSize = 64 << 10
	// maxAttestationMetadataSize bounds the attestation API response.
	maxAttestationMetadataSize = 4 << 20
	checksumsAssetName         = "checksums.txt"

	// maxArchiveSize bounds the downloaded archive; the binary inside it is bounded by the same limit.
	maxArchiveSize = maxBinarySize

	metadataTimeout = httpTimeout
	downloadTimeout = 2 * time.Minute
	ghTimeout       = 2 * time.Minute
	// maxGHOutput bounds the output captured from gh; maxGHDetail bounds what an error repeats.
	maxGHOutput = 16 << 10
	maxGHDetail = 2 << 10
	// maxRedirects is the redirect chain cap of the production client.
	maxRedirects = 10

	releaseRepo    = "seznam/jailoc"
	releaseRepoURL = "https://github.com/" + releaseRepo
	ghHostname     = "github.com"
	oidcIssuer     = "https://token.actions.githubusercontent.com"
	mainRef        = "refs/heads/main"

	inTotoStatementType = "https://in-toto.io/Statement/v1"
	inTotoPayloadType   = "application/vnd.in-toto+json"
	slsaPredicateType   = "https://slsa.dev/provenance/v1"

	ghMissingWarning = "gh (GitHub CLI) was not found: the release attestation was only matched against what the GitHub API reports, " +
		"so the publisher signature was NOT checked and the update relies on GitHub's TLS and API; install gh to verify signatures"
)

// releaseWorkflows are the workflows that may attest a release archive.
var releaseWorkflows = []string{
	".github/workflows/release-please.yml",
	".github/workflows/release.yml",
	".github/workflows/prerelease.yml",
}

// redirectHosts are the hosts a release download may be redirected to.
var redirectHosts = map[string]bool{
	"github.com":                           true,
	"api.github.com":                       true,
	"release-assets.githubusercontent.com": true,
	"objects.githubusercontent.com":        true,
}

// workflowIdentity is the workflow a release attestation must name.
type workflowIdentity struct {
	Repository string `json:"repository"`
	Path       string `json:"path"`
	Ref        string `json:"ref"`
}

// ghClient runs the optional GitHub CLI; both functions are replaced in tests.
type ghClient struct {
	lookPath func(file string) (string, error)
	// run returns the combined, bounded stdout and stderr of the command.
	run func(ctx context.Context, path string, args ...string) ([]byte, error)
}

func defaultGH() ghClient {
	return ghClient{lookPath: exec.LookPath, run: runGH}
}

// verifyResult reports what provenance verification established.
type verifyResult struct {
	// Signature is true only when gh verified the digest, certificate chain, signature and timestamps.
	Signature bool
	Warning   string
}

// newReleaseClient is the production HTTP client: bounded in time and following only
// HTTPS redirects between the GitHub hosts a release download uses.
func newReleaseClient() *http.Client {
	return &http.Client{Timeout: downloadTimeout, CheckRedirect: releaseRedirectPolicy}
}

// releaseRedirectPolicy limits redirect chains to maxRedirects HTTPS hops to redirectHosts and
// never forwards credentials to the redirect target.
func releaseRedirectPolicy(req *http.Request, via []*http.Request) error {
	switch {
	case len(via) >= maxRedirects:
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	case req.URL.Scheme != "https":
		return fmt.Errorf("redirect to %s URL refused: want https", req.URL.Scheme)
	case req.URL.User != nil:
		return errors.New("redirect URL with credentials refused")
	case !redirectHosts[strings.ToLower(req.URL.Hostname())]:
		return fmt.Errorf("redirect to host %q refused", req.URL.Hostname())
	case req.URL.Port() != "" && req.URL.Port() != "443":
		return fmt.Errorf("redirect to port %s refused", req.URL.Port())
	}

	req.Header.Del("Authorization")
	req.Header.Del("Cookie")

	return nil
}

func isLowerHex64(s string) bool {
	if len(s) != sha256.Size*2 {
		return false
	}

	for _, c := range []byte(s) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return true
}

// parseChecksumManifest returns the lowercase SHA-256 listed for filename in a checksums.txt body.
// Every line must be "<64 lowercase hex>  <filename>", and filename must be listed exactly once.
func parseChecksumManifest(data []byte, filename string) (string, error) {
	if len(data) > maxChecksumManifestSize {
		return "", fmt.Errorf("checksum manifest is %d bytes, exceeds the %d byte limit", len(data), maxChecksumManifestSize)
	}

	text := strings.TrimSuffix(string(data), "\n")
	if text == "" {
		return "", fmt.Errorf("no checksum for %q: manifest is empty", filename)
	}

	var found string
	var count int

	for i, line := range strings.Split(text, "\n") {
		sum, name, ok := splitChecksumLine(line)
		if !ok {
			return "", fmt.Errorf("malformed checksum manifest line %d", i+1)
		}
		if name == filename {
			found = sum
			count++
		}
	}

	switch {
	case count == 0:
		return "", fmt.Errorf("no checksum for %q in the manifest", filename)
	case count > 1:
		return "", fmt.Errorf("duplicate checksum entries for %q in the manifest", filename)
	}

	return found, nil
}

func splitChecksumLine(line string) (sum, name string, ok bool) {
	const nameOffset = sha256.Size*2 + 2

	if len(line) <= nameOffset || line[nameOffset-2:nameOffset] != "  " || !isLowerHex64(line[:nameOffset-2]) {
		return "", "", false
	}

	name = line[nameOffset:]
	if name[0] == ' ' || strings.ContainsAny(name, "\r\n") {
		return "", "", false
	}

	return line[:nameOffset-2], name, true
}

// assetSHA256 returns the SHA-256 GitHub advertises for asset, requiring a sha256 digest and a sane size.
func assetSHA256(asset githubAsset) (string, error) {
	sum, ok := strings.CutPrefix(asset.Digest, "sha256:")
	if !ok || !isLowerHex64(sum) {
		return "", fmt.Errorf("asset %q has no usable sha256 digest (got %q)", asset.Name, asset.Digest)
	}

	if asset.Size <= 0 || asset.Size > maxArchiveSize {
		return "", fmt.Errorf("asset %q size %d is outside 1..%d bytes", asset.Name, asset.Size, maxArchiveSize)
	}

	return sum, nil
}

func newGitHubRequest(ctx context.Context, rawURL, accept string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request for %s: %w", rawURL, err)
	}

	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Encoding", "identity") // keep the transport from decompressing what is hashed
	req.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	req.Header.Set("User-Agent", "jailoc-self-update")

	return req, nil
}

// fetchBounded reads one small response within metadataTimeout and at most limit bytes.
func fetchBounded(ctx context.Context, client *http.Client, rawURL, accept string, limit int64) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, metadataTimeout)
	defer cancel()

	req, err := newGitHubRequest(ctx, rawURL, accept)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req) //nolint:gosec // rawURL comes from release metadata or the configured release URL; redirects follow the client policy
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("request %s: unexpected status %d", rawURL, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response from %s exceeds the %d byte limit", rawURL, limit)
	}

	return data, nil
}

// downloadArchive streams the URL into a temporary file in dir, hashing as it goes, and keeps the
// file only when size and digest match. The archive is never decompressed here.
func downloadArchive(ctx context.Context, client *http.Client, rawURL string, size int64, digest, dir string) (file *os.File, err error) {
	if size <= 0 || size > maxArchiveSize {
		return nil, fmt.Errorf("advertised size %d is outside 1..%d bytes", size, maxArchiveSize)
	}

	req, err := newGitHubRequest(ctx, rawURL, "application/octet-stream")
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req) //nolint:gosec // rawURL comes from release metadata; redirects follow the client policy
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s: unexpected status %d", rawURL, resp.StatusCode)
	}
	if resp.ContentLength >= 0 && resp.ContentLength != size {
		return nil, fmt.Errorf("download %s: size mismatch: server sends %d bytes, advertised %d", rawURL, resp.ContentLength, size)
	}

	f, err := os.CreateTemp(dir, "archive-*.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("create archive file: %w", err)
	}
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}
	}()

	hash := sha256.New()

	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(resp.Body, size+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			err = ctxErr
		}

		return nil, fmt.Errorf("download %s: %w", rawURL, err)
	}

	if n > size {
		return nil, fmt.Errorf("download %s: size mismatch: more than the advertised %d bytes", rawURL, size)
	}
	if n != size {
		return nil, fmt.Errorf("download %s: size mismatch: got %d bytes, advertised %d", rawURL, n, size)
	}

	if got := hex.EncodeToString(hash.Sum(nil)); got != digest {
		return nil, fmt.Errorf("download %s: digest mismatch: got sha256:%s, want sha256:%s", rawURL, got, digest)
	}

	return f, nil
}

// attestationURL derives the attestation API URL of the repository that releaseURL lists.
func attestationURL(releaseURL, digest string) (string, error) {
	u, err := url.Parse(releaseURL)
	if err != nil {
		return "", fmt.Errorf("parse release URL %q: %w", releaseURL, err)
	}

	base, ok := strings.CutSuffix(strings.TrimSuffix(u.Path, "/"), "/releases")
	if !ok {
		return "", fmt.Errorf("release URL %q does not end in /releases", releaseURL)
	}

	u.Path, u.RawPath, u.Fragment = base, "", ""
	u = u.JoinPath("attestations", "sha256:"+digest)
	u.RawQuery = "per_page=100"

	return u.String(), nil
}

// fetchAttestations reads the attestations GitHub stores for the archive digest. The response is
// metadata only: matching it proves nothing cryptographically.
func fetchAttestations(ctx context.Context, client *http.Client, releaseURL, digest string) ([]byte, error) {
	rawURL, err := attestationURL(releaseURL, digest)
	if err != nil {
		return nil, err
	}

	data, err := fetchBounded(ctx, client, rawURL, "application/vnd.github+json", maxAttestationMetadataSize)
	if err != nil {
		return nil, fmt.Errorf("fetch attestations: %w", err)
	}

	return data, nil
}

type attestationSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type attestationStatement struct {
	Type          string               `json:"_type"`
	Subject       []attestationSubject `json:"subject"`
	PredicateType string               `json:"predicateType"`
	Predicate     struct {
		BuildDefinition struct {
			ExternalParameters struct {
				Workflow workflowIdentity `json:"workflow"`
			} `json:"externalParameters"`
		} `json:"buildDefinition"`
	} `json:"predicate"`
}

// decodeAttestations returns the in-toto statements of an attestation API response; entries that
// are not decodable statements are skipped.
func decodeAttestations(data []byte) ([]attestationStatement, error) {
	var list struct {
		Attestations []struct {
			Bundle struct {
				DSSEEnvelope struct {
					PayloadType string `json:"payloadType"`
					Payload     string `json:"payload"`
				} `json:"dsseEnvelope"`
			} `json:"bundle"`
		} `json:"attestations"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode attestation response: %w", err)
	}

	statements := make([]attestationStatement, 0, len(list.Attestations))

	for _, a := range list.Attestations {
		envelope := a.Bundle.DSSEEnvelope
		if envelope.PayloadType != inTotoPayloadType {
			continue
		}

		payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
		if err != nil {
			continue
		}

		var s attestationStatement
		if err := json.Unmarshal(payload, &s); err != nil {
			continue
		}

		statements = append(statements, s)
	}

	return statements, nil
}

// matches reports whether s is SLSA v1 provenance of exactly name and digest from one of wants.
func (s attestationStatement) matches(name, digest string, wants []workflowIdentity) bool {
	if s.Type != inTotoStatementType || s.PredicateType != slsaPredicateType {
		return false
	}

	subject := slices.ContainsFunc(s.Subject, func(sub attestationSubject) bool {
		return sub.Name == name && sub.Digest["sha256"] == digest
	})

	return subject && slices.Contains(wants, s.Predicate.BuildDefinition.ExternalParameters.Workflow)
}

func requireAttestation(data []byte, name, digest string, wants []workflowIdentity) error {
	statements, err := decodeAttestations(data)
	if err != nil {
		return err
	}

	if slices.ContainsFunc(statements, func(s attestationStatement) bool { return s.matches(name, digest, wants) }) {
		return nil
	}

	return fmt.Errorf("no attestation of %s sha256:%s from an expected release workflow among %d", name, digest, len(statements))
}

// checkAttestations requires one attestation whose SLSA subject and workflow match.
func checkAttestations(data []byte, name, digest string, want workflowIdentity) error {
	return requireAttestation(data, name, digest, []workflowIdentity{want})
}

// allowedWorkflows lists every workflow and ref that may have built the release tag.
func allowedWorkflows(tag string) []workflowIdentity {
	ids := make([]workflowIdentity, 0, len(releaseWorkflows)*2)

	for _, path := range releaseWorkflows {
		for _, ref := range []string{mainRef, "refs/tags/" + tag} {
			ids = append(ids, workflowIdentity{Repository: releaseRepoURL, Path: path, Ref: ref})
		}
	}

	return ids
}

// matchAttestations requires an attestation of the archive from any allowed workflow for tag.
func matchAttestations(data []byte, name, digest, tag string) error {
	return requireAttestation(data, name, digest, allowedWorkflows(tag))
}

// certIdentityRegex matches the certificate identity of any allowed workflow for tag, anchored.
func certIdentityRegex(tag string) string {
	paths := make([]string, 0, len(releaseWorkflows))
	for _, p := range releaseWorkflows {
		paths = append(paths, regexp.QuoteMeta(p))
	}

	return "^" + regexp.QuoteMeta(releaseRepoURL+"/") + "(" + strings.Join(paths, "|") + ")@(" +
		regexp.QuoteMeta(mainRef) + "|" + regexp.QuoteMeta("refs/tags/"+tag) + ")$"
}

func ghVerifyArgs(archivePath, tag string) []string {
	return []string{
		"attestation", "verify", archivePath,
		"--repo", releaseRepo,
		"--hostname", ghHostname,
		"--cert-identity-regex", certIdentityRegex(tag),
		"--cert-oidc-issuer", oidcIssuer,
		"--deny-self-hosted-runners",
	}
}

// verifyProvenance has the GitHub CLI verify the archive's attestation: digest, certificate chain,
// signature and timestamps. A missing gh is a warning; a gh that fails is fatal. gh's output is
// informational and never parsed.
func verifyProvenance(ctx context.Context, gh ghClient, archivePath, tag string) (verifyResult, error) {
	if err := ctx.Err(); err != nil {
		return verifyResult{}, fmt.Errorf("verify provenance: %w", err)
	}

	if gh.lookPath == nil || gh.run == nil {
		return verifyResult{Warning: ghMissingWarning}, nil
	}

	path, err := gh.lookPath("gh")
	if err != nil {
		return verifyResult{Warning: ghMissingWarning}, nil //nolint:nilerr // a missing gh is the documented warning case
	}

	out, err := gh.run(ctx, path, ghVerifyArgs(archivePath, tag)...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return verifyResult{}, fmt.Errorf("gh attestation verify: %w", ctxErr)
		}

		return verifyResult{}, fmt.Errorf("gh attestation verify failed: %w: %s", err, ghDetail(out))
	}

	return verifyResult{Signature: true}, nil
}

// ghDetail bounds and sanitizes gh output for an error message.
func ghDetail(out []byte) string {
	s := strings.ToValidUTF8(string(out), "?")
	s = strings.Map(func(r rune) rune {
		if (r < ' ' && r != '\n' && r != '\t') || r == 0x7f {
			return ' '
		}

		return r
	}, strings.TrimSpace(s))

	if len(s) > maxGHDetail {
		s = strings.ToValidUTF8(s[:maxGHDetail], "") + "... (truncated)"
	}

	return s
}

// cappedBuffer keeps the first limit bytes written to it and discards the rest.
type cappedBuffer struct {
	buf   []byte
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.buf); room > 0 {
		b.buf = append(b.buf, p[:min(room, len(p))]...)
	}

	return len(p), nil
}

// runGH runs gh without a shell, with prompts and the update notifier disabled, and returns its
// combined stdout and stderr capped at maxGHOutput.
func runGH(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ghTimeout)
	defer cancel()

	out := &cappedBuffer{limit: maxGHOutput}

	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // path comes from LookPath("gh") and args from ghVerifyArgs
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1", "GH_PAGER=cat")
	cmd.WaitDelay = 5 * time.Second

	err := cmd.Run()
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s: %w", ghTimeout, err)
	}

	return bytes.Clone(out.buf), err
}

// fetchVerifiedArchive downloads asset into dir and verifies its integrity and provenance. The
// advertised digest must agree with checksums.txt before anything is downloaded, an attestation
// of the same digest must come from an allowed release workflow, and gh, when present, verifies
// the downloaded file cryptographically.
func fetchVerifiedArchive(ctx context.Context, d deps, rel githubRelease, asset githubAsset, dir string) (_ *os.File, _ verifyResult, err error) {
	client := d.client
	if client == nil {
		client = newReleaseClient()
	}

	digest, err := assetSHA256(asset)
	if err != nil {
		return nil, verifyResult{}, fmt.Errorf("check release metadata: %w", err)
	}

	manifestAsset, err := findAsset(rel, checksumsAssetName)
	if err != nil {
		return nil, verifyResult{}, err
	}

	manifest, err := fetchBounded(ctx, client, manifestAsset.BrowserDownloadURL, "application/octet-stream", maxChecksumManifestSize)
	if err != nil {
		return nil, verifyResult{}, fmt.Errorf("fetch %s: %w", checksumsAssetName, err)
	}

	listed, err := parseChecksumManifest(manifest, asset.Name)
	if err != nil {
		return nil, verifyResult{}, fmt.Errorf("read %s: %w", checksumsAssetName, err)
	}
	if listed != digest {
		return nil, verifyResult{}, fmt.Errorf("integrity check failed: %s lists sha256:%s for %s but the release advertises sha256:%s",
			checksumsAssetName, listed, asset.Name, digest)
	}

	attestations, err := fetchAttestations(ctx, client, d.releaseURL, digest)
	if err != nil {
		return nil, verifyResult{}, err
	}
	if err := matchAttestations(attestations, asset.Name, digest, rel.TagName); err != nil {
		return nil, verifyResult{}, fmt.Errorf("provenance check failed: %w", err)
	}

	dlCtx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	file, err := downloadArchive(dlCtx, client, asset.BrowserDownloadURL, asset.Size, digest, dir)
	if err != nil {
		return nil, verifyResult{}, err
	}
	defer func() {
		if err != nil {
			_ = file.Close()
			_ = os.Remove(file.Name())
		}
	}()

	path, err := filepath.Abs(file.Name())
	if err != nil {
		return nil, verifyResult{}, fmt.Errorf("resolve archive path: %w", err)
	}

	res, err := verifyProvenance(ctx, d.gh, path, rel.TagName)
	if err != nil {
		return nil, verifyResult{}, err
	}

	return file, res, nil
}
