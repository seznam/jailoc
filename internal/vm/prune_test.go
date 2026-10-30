package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneImages(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	past := time.Now().Add(-2 * time.Hour)
	digest := func(n string) string { return "sha256:" + n + "0000" }
	addImage := func(name string, old bool) string {
		p := filepath.Join(dir, "sha256-"+name+"0000")
		if err := os.MkdirAll(filepath.Join(p, "etc", "ro"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(p, "etc", "ro"), 0o500); err != nil { //nolint:gosec // G302: read-only directory under test
			t.Fatal(err)
		}
		if old {
			if err := os.Chtimes(p, past, past); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	addRef := func(ref, dg string, old bool) string {
		h := sha256.Sum256([]byte(ref))
		p := filepath.Join(dir, "refs", hex.EncodeToString(h[:]))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(ref+"\t"+dg+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if old {
			if err := os.Chtimes(p, past, past); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	stale := addImage("aa", true)
	staleRef := addRef(RootFSRef("aa"), digest("aa"), true)
	current := addImage("bb", true)
	currentRef := addRef(RootFSRef("bb"), digest("bb"), true)
	builder := addImage("cc", true)
	builderRef := addRef("docker@sha256:feed", digest("cc"), true)
	fresh := addImage("dd", false)
	freshRef := addRef(RootFSRef("dd"), digest("dd"), false)
	orphanOld := addImage("ee", true)
	orphanNew := addImage("ff", false)
	snapshot := filepath.Join(dir, "snapshots", "sha256-0f0f")
	if err := os.MkdirAll(snapshot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(snapshot, past, past); err != nil {
		t.Fatal(err)
	}

	res, err := PruneImages(dir, []string{RootFSRef("bb")})
	if err != nil {
		t.Fatalf("PruneImages: %v", err)
	}
	if res.Refs != 1 || res.Images != 2 {
		t.Fatalf("result = %+v, want 1 reference and 2 images removed", res)
	}
	for _, p := range []string{stale, staleRef, orphanOld, snapshot} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", p, err)
		}
	}
	for _, p := range []string{current, currentRef, builder, builderRef, fresh, freshRef, orphanNew} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
	for _, p := range []string{current, builder, fresh, orphanNew} {
		_ = os.Chmod(filepath.Join(p, "etc", "ro"), 0o700) //nolint:gosec // G302: test cleanup
	}
}
