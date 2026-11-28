package update

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	goversion "github.com/hashicorp/go-version"
)

// Options selects what SelfUpdate does.
type Options struct {
	// Check reports the update without installing it.
	Check bool
	// Version installs this exact release tag; it may be a pre-release or a downgrade and skips the cooldown.
	Version string
	// NoCooldown offers releases younger than the cooldown.
	NoCooldown bool
	// Force installs over executables that are not plain release installs and reinstalls the current version.
	Force bool
}

// Report describes the outcome of SelfUpdate.
type Report struct {
	// Installed is true once the executable was replaced.
	Installed bool
	Current   string
	// Target is the release picked, or empty when no eligible release is newer than Current.
	Target string
	// Pending is a newer stable release still within the cooldown.
	Pending     string
	PublishedAt time.Time
	// Path is the resolved executable path.
	Path              string
	VerifiedSignature bool
	// Warning lists non-fatal problems, including ones after a successful install.
	Warning string
	// Installation explains why this executable cannot be self-updated; empty when it can.
	Installation string
}

// deps are the replaceable parts of SelfUpdate; production code uses defaultDeps.
type deps struct {
	releaseURL string
	statePath  string
	now        func() time.Time
	executable func() (string, error)
	// fetchArchive downloads and verifies the release archive into dir; nil selects fetchVerifiedArchive.
	fetchArchive func(ctx context.Context, rel githubRelease, asset githubAsset, dir string) (*os.File, verifyResult, error)
	replace      func(ctx context.Context, target, tmp string) error
	gh           ghClient
	client       *http.Client
}

func defaultDeps() deps {
	return deps{
		releaseURL: DefaultReleaseURL,
		statePath:  defaultStatePath(),
		now:        time.Now,
		executable: os.Executable,
		replace:    replaceExecutable,
		client:     newReleaseClient(),
		gh:         defaultGH(),
	}
}

// SelfUpdate replaces the running executable with the selected release, or only reports it with opts.Check.
func SelfUpdate(ctx context.Context, current string, opts Options) (Report, error) {
	return selfUpdate(ctx, current, opts, defaultDeps())
}

func selfUpdate(ctx context.Context, current string, opts Options, d deps) (Report, error) {
	rep := Report{Current: current}

	if err := ctx.Err(); err != nil {
		return rep, fmt.Errorf("start update: %w", err)
	}

	explicit := ""
	if opts.Version != "" {
		tag, err := normalizeTag(opts.Version)
		if err != nil {
			return rep, fmt.Errorf("parse version: %w", err)
		}
		explicit = tag
	}

	initial, err := inspectInstall(&rep, current, opts, explicit != "", d)
	if err != nil {
		return rep, err
	}

	var tag string
	if explicit != "" {
		tag = explicit
		rep.Target = tag
	} else {
		var done bool
		if tag, done, err = selectDefault(ctx, &rep, current, opts, d); err != nil || done {
			return rep, err
		}
	}

	if sameRelease(current, tag) && !opts.Force {
		return rep, nil
	}

	if opts.Check {
		if explicit == "" {
			return rep, nil
		}
		rel, err := exactRelease(ctx, d, tag, explicit != "", opts.NoCooldown)
		if err != nil {
			return rep, err
		}
		rep.PublishedAt = rel.PublishedAt

		return rep, nil
	}

	return install(ctx, rep, initial, tag, explicit != "", opts, d)
}

// inspectInstall resolves the executable and applies the install guards. They are fatal
// before any network access, except for a check, which records the reason in the report.
// The returned FileInfo identifies the executable at inspection time.
func inspectInstall(rep *Report, current string, opts Options, explicit bool, d deps) (os.FileInfo, error) {
	path, info, err := resolveExecutable(d.executable)
	if err == nil {
		rep.Path = path
		err = checkInstallGuards(path, current, opts.Force, explicit)
	}
	if err == nil {
		return info, nil
	}
	if opts.Check {
		rep.Installation = err.Error()

		return nil, nil
	}

	return nil, err
}

// selectDefault picks the highest eligible release from the cached candidates. done is true
// when there is nothing to install or check further.
func selectDefault(ctx context.Context, rep *Report, current string, opts Options, d deps) (tag string, done bool, err error) {
	now := d.now()

	candidates, err := releaseCandidates(ctx, d.releaseURL, d.statePath, now)
	if err != nil {
		return "", false, fmt.Errorf("find latest release: %w", err)
	}

	eligible, pending := selectRelease(candidates, now, opts.NoCooldown)

	currentV, currentErr := goversion.NewVersion(current)
	if pending != nil && currentErr == nil {
		if pendingV, err := goversion.NewVersion(pending.Tag); err != nil || !pendingV.GreaterThan(currentV) {
			pending = nil
		}
	}
	if pending != nil {
		rep.Pending = pending.Tag
	}

	if eligible == nil {
		return "", true, nil
	}

	if currentErr == nil {
		if eligibleV, err := goversion.NewVersion(eligible.Tag); err == nil && eligibleV.LessThan(currentV) {
			return "", true, nil
		}
	}

	rep.Target = eligible.Tag
	rep.PublishedAt = eligible.PublishedAt

	return eligible.Tag, false, nil
}

// exactRelease fetches the release metadata for tag and fails closed unless it is still eligible now.
func exactRelease(ctx context.Context, d deps, tag string, explicit, noCooldown bool) (githubRelease, error) {
	rel, err := fetchRelease(ctx, d.releaseURL, tag)
	if err != nil {
		return githubRelease{}, fmt.Errorf("fetch release %s: %w", tag, err)
	}

	now := d.now()

	switch {
	case rel.Draft:
		return githubRelease{}, fmt.Errorf("release %s is a draft", tag)
	case rel.PublishedAt.After(now):
		return githubRelease{}, fmt.Errorf("release %s is published in the future", tag)
	case explicit:
		return rel, nil
	case rel.Prerelease || strings.Contains(tag, "-"):
		return githubRelease{}, fmt.Errorf("release %s is a pre-release; pass --version to install it", tag)
	case !noCooldown && now.Sub(rel.PublishedAt) < releaseCooldown:
		return githubRelease{}, fmt.Errorf("release %s is within the %s cooldown", tag, releaseCooldown)
	}

	return rel, nil
}

// sameRelease compares versions ignoring a leading "v"; non-versions such as "dev" never match.
func sameRelease(current, tag string) bool {
	a, errA := goversion.NewVersion(current)
	b, errB := goversion.NewVersion(tag)

	return errA == nil && errB == nil && a.Equal(b)
}

// archiveAssetName is the release archive for tag on this platform.
func archiveAssetName(tag string) string {
	return fmt.Sprintf("jailoc_%s_%s_%s.tar.gz", strings.TrimPrefix(tag, "v"), runtime.GOOS, runtime.GOARCH)
}

func findAsset(rel githubRelease, name string) (githubAsset, error) {
	var found []githubAsset

	for _, a := range rel.Assets {
		if a.Name == name {
			found = append(found, a)
		}
	}

	if len(found) != 1 {
		return githubAsset{}, fmt.Errorf("release %s has %d assets named %q, want exactly 1", rel.TagName, len(found), name)
	}

	return found[0], nil
}

func install(ctx context.Context, rep Report, initial os.FileInfo, tag string, explicit bool, opts Options, d deps) (Report, error) {
	rel, err := exactRelease(ctx, d, tag, explicit, opts.NoCooldown)
	if err != nil {
		return rep, err
	}

	rep.PublishedAt = rel.PublishedAt

	asset, err := findAsset(rel, archiveAssetName(rel.TagName))
	if err != nil {
		return rep, err
	}

	workDir, err := os.MkdirTemp("", "jailoc-update-*")
	if err != nil {
		return rep, fmt.Errorf("create download directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()

	fetch := d.fetchArchive
	if fetch == nil {
		fetch = func(ctx context.Context, rel githubRelease, asset githubAsset, dir string) (*os.File, verifyResult, error) {
			return fetchVerifiedArchive(ctx, d, rel, asset, dir)
		}
	}

	archive, verified, err := fetch(ctx, rel, asset, workDir)
	if err != nil {
		return rep, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer func() { _ = archive.Close() }()

	tmp, err := extractBinary(archive, filepath.Dir(rep.Path))
	if err != nil {
		return rep, fmt.Errorf("unpack %s: %w", asset.Name, err)
	}

	warnings := []string{}
	if verified.Warning != "" {
		warnings = append(warnings, verified.Warning)
	}

	if err := replaceIfUnchanged(ctx, d.replace, rep.Path, initial, tmp); err != nil {
		if !errors.Is(err, ErrInstalledSyncFailed) {
			if rmErr := os.Remove(tmp); rmErr != nil && !errors.Is(rmErr, fs.ErrNotExist) {
				err = errors.Join(err, rmErr)
			}

			return rep, fmt.Errorf("install %s: %w", tag, err)
		}
		warnings = append(warnings, fmt.Sprintf("executable replaced but not yet durable: %v", err))
	}

	rep.Installed = true
	rep.VerifiedSignature = verified.Signature

	// The cache predates this install; dropping it is best effort.
	if err := os.Remove(d.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		warnings = append(warnings, fmt.Sprintf("could not clear the update cache: %v", err))
	}

	rep.Warning = strings.Join(warnings, "; ")

	return rep, nil
}
