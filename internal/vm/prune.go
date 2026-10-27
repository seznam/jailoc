package vm

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/go-microvm/image"
)

// Image cache pruning keeps entries younger than pruneGrace: another jailoc
// process may have unpacked an image it has not recorded in a spec yet, and a
// VM supervisor may be pulling its base image right now.
const (
	pruneGrace       = time.Hour
	layerUnusedLimit = 14 * 24 * time.Hour
)

// PruneResult reports what PruneImages removed.
type PruneResult struct {
	Refs, Images, Layers int
}

// PruneImages drops workspace images from the image cache at cacheDir that
// no reference in keep names, and the unpacked images no reference points
// to any more. Extracted layers unused for two weeks are removed as well.
// Only workspace image references (RootFSRef) are dropped; other references
// such as the builder image stay. Entries changed within the last hour are
// kept.
func PruneImages(cacheDir string, keep []string) (PruneResult, error) {
	var res PruneResult
	unlock, err := lockCache(cacheDir)
	if err != nil {
		return res, err
	}
	defer unlock()

	kept := make(map[string]bool, len(keep))
	for _, ref := range keep {
		kept[ref] = true
	}
	cutoff := time.Now().Add(-pruneGrace)
	old := func(path string) bool {
		info, err := os.Lstat(path)
		return err == nil && info.ModTime().Before(cutoff)
	}

	refsDir := filepath.Join(cacheDir, "refs")
	entries, err := os.ReadDir(refsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("read image cache references: %w", err)
	}
	live := map[string]bool{}
	for _, e := range entries {
		path := filepath.Join(refsDir, e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // G304: file inside jailoc's image cache
		if err != nil || e.IsDir() {
			continue
		}
		ref, digest := parseRef(data)
		if strings.HasPrefix(ref, rootfsRepo+":") && !kept[ref] && old(path) {
			if err := os.Remove(path); err != nil {
				return res, fmt.Errorf("remove image reference %s: %w", ref, err)
			}
			res.Refs++
			continue
		}
		if digest != "" {
			live[digest] = true
		}
	}

	dirs, err := os.ReadDir(cacheDir)
	if err != nil {
		return res, fmt.Errorf("read image cache: %w", err)
	}
	for _, e := range dirs {
		name := e.Name()
		path := filepath.Join(cacheDir, name)
		if !e.IsDir() || !strings.HasPrefix(name, "sha256-") || live[strings.Replace(name, "-", ":", 1)] || !old(path) {
			continue
		}
		if err := removeTree(path); err != nil {
			return res, fmt.Errorf("remove unpacked image %s: %w", name, err)
		}
		res.Images++
	}

	n, err := image.NewCache(cacheDir).LayerCache().Evict(layerUnusedLimit)
	res.Layers = n
	if err != nil {
		return res, fmt.Errorf("remove unused image layers: %w", err)
	}
	return res, nil
}

// parseRef reads an image cache reference file: "ref\tdigest", or a bare
// digest in older caches.
func parseRef(data []byte) (ref, digest string) {
	content := strings.TrimSpace(string(data))
	if before, after, ok := strings.Cut(content, "\t"); ok {
		return before, after
	}
	return "", content
}

// removeTree deletes path, first making directories writable: unpacked
// images keep read-only directories from the image.
func removeTree(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700) //nolint:gosec // G302: owner-only, directory is about to be removed
		}
		return nil
	})
	return os.RemoveAll(path)
}

// lockCache serializes image cache imports and pruning between jailoc
// processes.
func lockCache(cacheDir string) (func(), error) {
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("create image cache: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(cacheDir, ".jailoc.lock"), os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: lock file inside jailoc's image cache
	if err != nil {
		return nil, fmt.Errorf("open image cache lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: file descriptors fit in int
		_ = f.Close()
		return nil, fmt.Errorf("lock image cache: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
