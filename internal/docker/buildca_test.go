package docker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/moby/buildkit/session/secrets/secretsprovider"
)

func TestBuildCAEmptyInput(t *testing.T) {
	t.Parallel()

	for name, pem := range map[string][]byte{"nil": nil, "empty": {}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ca, err := NewBuildCA(pem)
			if err != nil {
				t.Fatalf("NewBuildCA: %v", err)
			}
			if ca != nil {
				t.Fatalf("NewBuildCA = %+v, want nil", ca)
			}
		})
	}
}

func TestBuildCANilReceiver(t *testing.T) {
	t.Parallel()

	var ca *BuildCA
	if got := ca.Digest(); got != "" {
		t.Errorf("Digest() = %q, want empty", got)
	}
	if got := ca.PEM(); got != nil {
		t.Errorf("PEM() = %q, want nil", got)
	}
	if got := ca.secrets(); got != nil {
		t.Errorf("secrets() = %v, want nil", got)
	}
	if got := ca.tagSuffix(); got != "" {
		t.Errorf("tagSuffix() = %q, want empty", got)
	}

	v := "1"
	args := map[string]*string{"A": &v}
	got := ca.hashArgs(args)
	if len(got) != 1 || got["A"] != &v {
		t.Errorf("hashArgs() = %v, want the caller's args", got)
	}
	if got := ca.hashArgs(nil); got != nil {
		t.Errorf("hashArgs(nil) = %v, want nil", got)
	}
}

func TestBuildCADigestAndTagSuffix(t *testing.T) {
	t.Parallel()

	pem := []byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n")
	ca, err := NewBuildCA(pem)
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	sum := sha256.Sum256(pem)
	want := hex.EncodeToString(sum[:])
	if got := ca.Digest(); got != want {
		t.Errorf("Digest() = %q, want %q", got, want)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(ca.Digest()) {
		t.Errorf("Digest() = %q, want 64 hex characters", ca.Digest())
	}
	if got, wantSuffix := ca.tagSuffix(), "-ca-"+want[:12]; got != wantSuffix {
		t.Errorf("tagSuffix() = %q, want %q", got, wantSuffix)
	}
	if !regexp.MustCompile(`^-ca-[0-9a-f]{12}$`).MatchString(ca.tagSuffix()) {
		t.Errorf("tagSuffix() = %q, want -ca-<12 hex>", ca.tagSuffix())
	}

	again, err := NewBuildCA(bytes.Clone(pem))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	if again.Digest() != ca.Digest() || again.tagSuffix() != ca.tagSuffix() {
		t.Error("same bundle produced a different digest or tag suffix")
	}
}

func TestBuildCAEditChangesDigest(t *testing.T) {
	t.Parallel()

	a, err := NewBuildCA([]byte("bundle one"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	b, err := NewBuildCA([]byte("bundle two"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	if a.Digest() == b.Digest() {
		t.Error("different bundles share a digest")
	}
	if a.tagSuffix() == b.tagSuffix() {
		t.Error("different bundles share a tag suffix")
	}
}

func TestBuildCADefensiveCopies(t *testing.T) {
	t.Parallel()

	in := []byte("original bundle")
	ca, err := NewBuildCA(in)
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	in[0] = 'X'
	if got := string(ca.PEM()); got != "original bundle" {
		t.Errorf("PEM() = %q after mutating input, want original", got)
	}

	out := ca.PEM()
	out[0] = 'Y'
	if got := string(ca.PEM()); got != "original bundle" {
		t.Errorf("PEM() = %q after mutating returned slice, want original", got)
	}

	secrets := ca.secrets()
	if len(secrets) != 1 {
		t.Fatalf("secrets() has %d entries, want 1", len(secrets))
	}
	if got := string(secrets[caSecretID]); got != "original bundle" {
		t.Errorf("secrets()[%q] = %q, want original", caSecretID, got)
	}
	secrets[caSecretID][0] = 'Z'
	if got := string(ca.secrets()[caSecretID]); got != "original bundle" {
		t.Errorf("secrets() = %q after mutating returned slice, want original", got)
	}
}

func TestBuildCASizeLimit(t *testing.T) {
	t.Parallel()

	t.Run("at limit", func(t *testing.T) {
		t.Parallel()
		ca, err := NewBuildCA(bytes.Repeat([]byte("a"), secretsprovider.MaxSecretSize))
		if err != nil {
			t.Fatalf("NewBuildCA: %v", err)
		}
		if ca == nil {
			t.Fatal("NewBuildCA = nil, want a BuildCA")
		}
	})

	t.Run("over limit", func(t *testing.T) {
		t.Parallel()
		ca, err := NewBuildCA(bytes.Repeat([]byte("a"), secretsprovider.MaxSecretSize+1))
		if err == nil {
			t.Fatal("NewBuildCA succeeded, want size error")
		}
		if ca != nil {
			t.Errorf("NewBuildCA = %+v, want nil", ca)
		}
		for _, want := range []string{"500 KiB", "ca_bundle = false"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
	})
}

func TestBuildCAHashArgs(t *testing.T) {
	t.Parallel()

	v := "1"
	args := map[string]*string{"A": &v}
	dockerfile := []byte("FROM scratch\n")

	ca, err := NewBuildCA([]byte("bundle one"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}
	other, err := NewBuildCA([]byte("bundle two"))
	if err != nil {
		t.Fatalf("NewBuildCA: %v", err)
	}

	got := ca.hashArgs(args)
	if _, ok := args[caDigestArg]; ok || len(args) != 1 {
		t.Errorf("hashArgs mutated the caller's map: %v", args)
	}
	if d := got[caDigestArg]; d == nil || *d != ca.Digest() {
		t.Errorf("hashArgs()[%q] = %v, want %q", caDigestArg, d, ca.Digest())
	}
	if got["A"] != &v {
		t.Error("hashArgs dropped the caller's args")
	}

	if fromNil := ca.hashArgs(nil); fromNil[caDigestArg] == nil || *fromNil[caDigestArg] != ca.Digest() {
		t.Errorf("hashArgs(nil) = %v, want the digest arg", fromNil)
	}

	plain := contentHash(dockerfile, args)
	withCA := contentHash(dockerfile, ca.hashArgs(args))
	withOther := contentHash(dockerfile, other.hashArgs(args))
	if plain == withCA {
		t.Error("contentHash is unchanged by setting a CA")
	}
	if withCA == withOther {
		t.Error("contentHash is unchanged by changing the CA")
	}
	if withCA != contentHash(dockerfile, ca.hashArgs(args)) {
		t.Error("contentHash is not deterministic for the same CA")
	}
}
