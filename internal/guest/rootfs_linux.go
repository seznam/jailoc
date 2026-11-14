//go:build linux

package guest

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"
)

// Directories the overlay root is assembled from, in the boot root file
// system.
const (
	overlayDir    = Dir + "/overlay"
	imageShareDir = overlayDir + "/share" // the image share as mounted
	bootLowerDir  = overlayDir + "/boot"  // idmapped boot root
	imageLowerDir = overlayDir + "/image" // idmapped image share
	homeLowerDir  = overlayDir + "/home"  // the image's agent home, idmapped to the agent
	upperDir      = overlayDir + "/rw"    // tmpfs with the upper and work directories
)

// overlayLayers are the lower layers of the root overlay. home is empty when
// the agent's home needs no overlay of its own.
type overlayLayers struct{ boot, image, home string }

// overlayRoot replaces the boot root file system, which holds only jailoc's
// files, with an overlay of it over the image shared read-only under
// cfg.RootShare. Writes go to a tmpfs and last until the VM stops, like the
// copy of the image the boot root replaces. With cfg.IDMap the layers are
// idmapped as in idmapRoot and the returned idmapper serves the shares; when
// the kernel cannot idmap, cfg takes the host user's IDs instead.
func overlayRoot(logger *slog.Logger, cfg *Config) (*idmapper, error) {
	for _, dir := range []string{imageShareDir, bootLowerDir, imageLowerDir, homeLowerDir, upperDir, newRootDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	// No nosuid: the image's setuid programs, such as sudo, keep working.
	if err := unix.Mount(cfg.RootShare, imageShareDir, "virtiofs", unix.MS_RDONLY|unix.MS_NODEV, ""); err != nil {
		return nil, fmt.Errorf("mount image share: %w", err)
	}
	// pivot_root refuses a shared parent mount.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return nil, fmt.Errorf("make / private: %w", err)
	}
	if err := unix.Mount("tmpfs", upperDir, "tmpfs", 0, "mode=0755"); err != nil {
		return nil, fmt.Errorf("mount overlay upper tmpfs: %w", err)
	}

	var mapper *idmapper
	layers := overlayLayers{boot: "/", image: imageShareDir}
	if cfg.IDMap != nil {
		m, idmapped, err := idmappedLayers(*cfg.IDMap, cfg.Home, cfg.UID, cfg.GID)
		if err != nil {
			// Without idmapped mounts the agent runs with the host user's IDs,
			// so its files on virtio-fs keep their owner.
			logger.Warn("idmapped mounts unavailable; running the agent with the host user's IDs", "error", err)
			cfg.UID, cfg.GID = cfg.IDMap.UID, cfg.IDMap.GID
			cfg.IDMap = nil
			cfg.hostIDs = true
		} else {
			mapper, layers = m, idmapped
		}
	}

	if err := mountOverlay(newRootDir, filepath.Join(upperDir, "root"), layers.boot, layers.image); err != nil {
		mapper.close()
		return nil, err
	}
	if layers.home != "" {
		if err := mountHomeOverlay(filepath.Join(newRootDir, cfg.Home), filepath.Join(upperDir, "home"), layers.home); err != nil {
			_ = unix.Unmount(newRootDir, unix.MNT_DETACH)
			mapper.close()
			return nil, err
		}
	}
	if err := switchRoot(newRootDir); err != nil {
		mapper.close()
		return nil, err
	}
	logger.Info("overlay root filesystem", "idmapped", mapper != nil)
	return mapper, nil
}

// idmappedLayers attaches the boot root and the image idmapped so that the
// host user's files belong to root, and the image's agent home, when it has
// one, so that they belong to the agent. Nothing stays attached when it
// fails.
func idmappedLayers(m IDMap, home string, agentUID, agentGID uint32) (_ *idmapper, _ overlayLayers, err error) {
	// Looking up the home also waits for the image share's FUSE INIT reply,
	// which idmapped mounts require.
	imageHome := filepath.Join(imageShareDir, home)
	info, statErr := os.Stat(imageHome)
	hasHome := statErr == nil && info.IsDir()

	rootNS, sharesNS, err := idNamespaces(m, agentUID, agentGID)
	if err != nil {
		return nil, overlayLayers{}, err
	}
	defer func() { _ = rootNS.Close() }()
	var attached []string
	defer func() {
		if err != nil {
			for _, dir := range slices.Backward(attached) {
				_ = unix.Unmount(dir, unix.MNT_DETACH)
			}
			_ = sharesNS.Close()
		}
	}()
	attach := func(src, dst string, ns *os.File) error {
		if err := attachIdmapped(src, dst, ns); err != nil {
			return fmt.Errorf("idmap %s: %w", src, err)
		}
		attached = append(attached, dst)
		return nil
	}
	layers := overlayLayers{boot: bootLowerDir, image: imageLowerDir}
	if err = attach("/", bootLowerDir, rootNS); err != nil {
		return nil, overlayLayers{}, err
	}
	if err = attach(imageShareDir, imageLowerDir, rootNS); err != nil {
		return nil, overlayLayers{}, err
	}
	if hasHome {
		if err = attach(imageHome, homeLowerDir, sharesNS); err != nil {
			return nil, overlayLayers{}, err
		}
		layers.home = homeLowerDir
	}
	return &idmapper{shares: sharesNS}, layers, nil
}

// mountOverlay mounts an overlay of lowers, the first on top, at target with
// its upper and work directories in dir.
func mountOverlay(target, dir string, lowers ...string) error {
	upper, work := filepath.Join(dir, "upper"), filepath.Join(dir, "work")
	for _, d := range []string{upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil { //nolint:gosec // G301: the upper directory becomes a world-readable root
			return fmt.Errorf("create %s: %w", d, err)
		}
	}
	if err := unix.Mount("overlay", target, "overlay", 0, overlayOptions(lowers, upper, work)); err != nil {
		return fmt.Errorf("mount overlay at %s: %w", target, err)
	}
	return nil
}

// mountHomeOverlay mounts an overlay of the agent home lower at target. Its
// upper directory takes the lower's owner and mode, because the overlay's
// root shows the upper directory.
func mountHomeOverlay(target, dir, lower string) error {
	var st unix.Stat_t
	if err := unix.Stat(lower, &st); err != nil {
		return fmt.Errorf("stat home: %w", err)
	}
	upper := filepath.Join(dir, "upper")
	if err := os.MkdirAll(upper, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", upper, err)
	}
	if err := os.Chown(upper, int(st.Uid), int(st.Gid)); err != nil {
		return fmt.Errorf("chown home upper: %w", err)
	}
	if err := os.Chmod(upper, os.FileMode(st.Mode&0o777)); err != nil {
		return fmt.Errorf("chmod home upper: %w", err)
	}
	return mountOverlay(target, dir, lower)
}

// overlayOptions returns overlayfs mount options for lowers, the first on
// top.
func overlayOptions(lowers []string, upper, work string) string {
	return "lowerdir=" + strings.Join(lowers, ":") + ",upperdir=" + upper + ",workdir=" + work
}
