package vm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	legacyRef := func(n string) string { return legacyRootfsRepo + ":" + n }
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
	staleRef := addRef(legacyRef("aa"), digest("aa"), true)
	current := addImage("bb", true)
	currentRef := addRef(legacyRef("bb"), digest("bb"), true)
	builder := addImage("cc", true)
	builderRef := addRef("docker@sha256:feed", digest("cc"), true)
	fresh := addImage("dd", false)
	freshRef := addRef(legacyRef("dd"), digest("dd"), false)
	orphanOld := addImage("ee", true)
	orphanNew := addImage("ff", false)
	snapshot := filepath.Join(dir, "snapshots", "sha256-0f0f")
	if err := os.MkdirAll(snapshot, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(snapshot, past, past); err != nil {
		t.Fatal(err)
	}

	res, err := PruneImages(dir, []string{legacyRef("bb")})
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

func TestPruneImagesEROFS(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	past := time.Now().Add(-2 * time.Hour)
	age := func(p string) {
		t.Helper()
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	layers := filepath.Join(dir, erofsDir, layersDir)
	images := filepath.Join(dir, erofsDir, imagesDir)
	for _, d := range []string{layers, images} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	addLayer := func(name string) string {
		p := filepath.Join(layers, name+".erofs")
		if err := os.WriteFile(p, []byte(name), 0o444); err != nil { //nolint:gosec // G306: read-only layer file like the cache writes
			t.Fatal(err)
		}
		age(p)
		return p
	}
	addImage := func(tag string, old bool, layers ...string) string {
		p := filepath.Join(images, tag)
		if err := os.Mkdir(p, 0o755); err != nil { //nolint:gosec // G301: image directory mode like the cache writes
			t.Fatal(err)
		}
		for i, l := range layers {
			if err := os.Link(l, filepath.Join(p, fmt.Sprintf("%03d.erofs", i))); err != nil {
				t.Fatal(err)
			}
		}
		if old {
			age(p)
		}
		return p
	}
	shared, staleOnly, freshOnly, unused := addLayer("shared"), addLayer("stale"), addLayer("fresh"), addLayer("unused")
	stale := addImage("aa", true, shared, staleOnly)
	current := addImage("bb", true, shared)
	fresh := addImage("cc", false, freshOnly)
	staging := filepath.Join(images, ".tmp-1")
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	age(staging)
	stagingLayer := filepath.Join(layers, ".tmp-1.erofs")
	if err := os.WriteFile(stagingLayer, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	age(stagingLayer)

	res, err := PruneImages(dir, []string{RootFSRef("bb")})
	if err != nil {
		t.Fatalf("PruneImages: %v", err)
	}
	if res.Images != 1 || res.Layers != 2 {
		t.Fatalf("result = %+v, want 1 image and 2 layers removed", res)
	}
	for _, p := range []string{stale, staleOnly, unused, staging, stagingLayer} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err %v)", p, err)
		}
	}
	for _, p := range []string{current, fresh, shared, freshOnly} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}

func TestPrivateCacheDir(t *testing.T) {
	t.Parallel()
	for name, existing := range map[string]bool{"missing": false, "existing": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := filepath.Join(t.TempDir(), "images")
			if existing {
				if err := os.Mkdir(dir, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			if err := privateCacheDir(dir); err != nil {
				t.Fatalf("privateCacheDir: %v", err)
			}
			info, err := os.Stat(dir)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o700 {
				t.Fatalf("mode = %o, want 700", perm)
			}
		})
	}
}

func TestRemoveTree(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "state")
	locked := filepath.Join(dir, "a", "locked")
	if err := os.MkdirAll(filepath.Join(locked, "b"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "b", "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	if err := RemoveTree(dir); err != nil {
		t.Fatalf("RemoveTree() = %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("Lstat after RemoveTree = %v, want not exist", err)
	}
}

func TestPruneCacheDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	past := time.Now().Add(-8 * 24 * time.Hour)
	recent := time.Now().Add(-2 * 24 * time.Hour)
	addDir := func(name string, mtime time.Time) string {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Join(p, "runtime"), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "runtime", RunnerBinary), []byte("x"), 0o755); err != nil { //nolint:gosec // G306: executable test fixture
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}

	staleUnused := addDir("1111111111111111", past)
	staleKept := addDir("2222222222222222", past)
	recentUnused := addDir("3333333333333333", recent)
	staging := addDir(".unpack-1234", past)

	removed, err := PruneCacheDirs(dir, []string{filepath.Join(staleKept, "runtime", "go-microvm-runner")})
	if err != nil {
		t.Fatalf("PruneCacheDirs: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(staleUnused); !os.IsNotExist(err) {
		t.Errorf("staleUnused still exists (err %v)", err)
	}
	for _, p := range []string{staleKept, recentUnused, staging} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was removed: %v", p, err)
		}
	}
}
