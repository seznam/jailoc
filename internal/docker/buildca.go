package docker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"

	"github.com/moby/buildkit/session/secrets/secretsprovider"
)

const (
	// caSecretID is the BuildKit secret ID the CA bundle is passed under.
	caSecretID = "jailoc-ca"
	// caTrustMarker marks the CA trust step of an image build.
	caTrustMarker = "jailoc-ca-trust"
	// caDigestArg is the build argument that carries the bundle digest, so a
	// changed bundle invalidates the build cache and the content hash.
	caDigestArg = "JAILOC_CA_DIGEST"
)

// BuildCA is a CA bundle forwarded into image builds as a BuildKit secret.
// A nil *BuildCA means no bundle is forwarded.
type BuildCA struct {
	pem    []byte
	digest string
}

// NewBuildCA wraps a PEM bundle for image builds. It returns nil for an empty
// bundle. The size limit of a BuildKit secret applies only to actual image
// builds; see ValidateSecretSize.
func NewBuildCA(pem []byte) (*BuildCA, error) {
	if len(pem) == 0 {
		return nil, nil
	}
	sum := sha256.Sum256(pem)
	return &BuildCA{pem: slices.Clone(pem), digest: hex.EncodeToString(sum[:])}, nil
}

// ValidateSecretSize rejects a bundle BuildKit cannot carry as a secret. Image
// builds call it before using the bundle; a nil BuildCA is always valid.
func (c *BuildCA) ValidateSecretSize() error {
	if c != nil && len(c.pem) > secretsprovider.MaxSecretSize {
		return fmt.Errorf("ca_bundle is %d bytes; image builds pass it as a BuildKit secret of at most %d KiB: use a smaller bundle or set ca_bundle = false", len(c.pem), secretsprovider.MaxSecretSize/1024)
	}
	return nil
}

// Digest returns the hex SHA-256 of the bundle, or "" for a nil BuildCA.
func (c *BuildCA) Digest() string {
	if c == nil {
		return ""
	}
	return c.digest
}

// PEM returns a copy of the bundle, or nil for a nil BuildCA.
func (c *BuildCA) PEM() []byte {
	if c == nil {
		return nil
	}
	return slices.Clone(c.pem)
}

// secrets returns the BuildKit secrets of a build, keyed by secret ID.
func (c *BuildCA) secrets() map[string][]byte {
	if c == nil {
		return nil
	}
	return map[string][]byte{caSecretID: slices.Clone(c.pem)}
}

// hashArgs returns args extended with the bundle digest for contentHash. It
// never mutates args.
func (c *BuildCA) hashArgs(args map[string]*string) map[string]*string {
	if c == nil {
		return args
	}
	cloned := maps.Clone(args)
	if cloned == nil {
		cloned = make(map[string]*string, 1)
	}
	d := c.digest
	cloned[caDigestArg] = &d
	return cloned
}

// tagSuffix returns the image tag suffix that separates builds with
// different bundles.
func (c *BuildCA) tagSuffix() string {
	if c == nil {
		return ""
	}
	return "-ca-" + c.digest[:12]
}

// EnsureBuildImage is EnsureImage for a build base image. With a CA bundle the
// build runs in the builder VM, which cannot see images that exist only in the
// host daemon, so a failure says so.
func EnsureBuildImage(ctx context.Context, ref string, ca *BuildCA) error {
	err := EnsureImage(ctx, ref)
	if err != nil && ca != nil {
		return fmt.Errorf("image %s is not available to the jailoc builder VM: with ca_bundle forwarding jailoc builds in the builder VM, which cannot use images that exist only in the host Docker daemon; push it to a registry the builder can reach, or set ca_bundle = false: %w", ref, err)
	}
	return err
}
