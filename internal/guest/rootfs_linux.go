//go:build linux

package guest

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// Directories the overlay root is assembled from, in the boot root file
// system.
const (
	overlayDir    = Dir + "/overlay"
	imageShareDir = overlayDir + "/share" // the share with the image's layer images
	layerDir      = overlayDir + "/l"     // one EROFS mount per layer, named by its index
	bootLowerDir  = overlayDir + "/boot"  // idmapped boot root
	upperDir      = overlayDir + "/rw"    // tmpfs with the upper and work directories
)

// maxMountData is the most option bytes mount(2) passes to a file system.
const maxMountData = 4095

// overlayRoot replaces the boot root file system, which holds only jailoc's
// files, with an overlay of it over the image's EROFS layer images in the
// share cfg.RootShare. Writes go to a tmpfs and last until the VM stops. The
// layer images keep the image's own owners, so only the boot root is
// idmapped, with cfg.IDMap, so that the host user's files belong to root;
// the returned idmapper serves the shares. When the kernel cannot idmap the
// boot root, cfg takes the host user's IDs instead, and the layers are
// idmapped so that the agent's files in the image belong to the host user.
func overlayRoot(logger *slog.Logger, cfg *Config) (*idmapper, error) {
	if len(cfg.RootLayers) == 0 {
		return nil, errors.New("guest config lists no root layers")
	}
	for _, dir := range []string{imageShareDir, bootLowerDir, upperDir, newRootDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := unix.Mount(cfg.RootShare, imageShareDir, "virtiofs", unix.MS_RDONLY|unix.MS_NODEV|unix.MS_NOSUID, ""); err != nil {
		return nil, fmt.Errorf("mount image share: %w", err)
	}
	// pivot_root refuses a shared parent mount.
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return nil, fmt.Errorf("make / private: %w", err)
	}
	if err := unix.Mount("tmpfs", upperDir, "tmpfs", 0, "mode=0755"); err != nil {
		return nil, fmt.Errorf("mount overlay upper tmpfs: %w", err)
	}
	layers, err := mountLayers(cfg.RootLayers)
	if err != nil {
		return nil, err
	}

	var mapper *idmapper
	boot := "/"
	if cfg.IDMap != nil {
		m, err := idmapBoot(*cfg.IDMap, cfg.UID, cfg.GID)
		if err != nil {
			// Without idmapped mounts the agent runs with the host user's IDs,
			// so its files on virtio-fs keep their owner.
			logger.Warn("idmapped mounts unavailable; running the agent with the host user's IDs", "error", err)
			if err := idmapLayers(layers, cfg.UID, cfg.GID, cfg.IDMap.UID, cfg.IDMap.GID); err != nil {
				logger.Warn("idmap image layers; the agent may not own its home directory", "error", err)
			}
			cfg.UID, cfg.GID = cfg.IDMap.UID, cfg.IDMap.GID
			cfg.IDMap = nil
		} else {
			mapper, boot = m, bootLowerDir
		}
	}

	lowers := []string{boot}
	for _, l := range slices.Backward(layers) {
		lowers = append(lowers, l)
	}
	if err := mountOverlay(newRootDir, filepath.Join(upperDir, "root"), lowers...); err != nil {
		mapper.close()
		return nil, err
	}
	if err := switchRoot(newRootDir); err != nil {
		mapper.close()
		return nil, err
	}
	logger.Info("overlay root filesystem", "layers", len(layers), "idmapped", mapper != nil)
	return mapper, nil
}

// mountLayers mounts the EROFS layer images in the image share read-only
// and returns their mount points, the bottom layer first. The kernel reads
// the images straight from the share, without loop devices.
func mountLayers(names []string) ([]string, error) {
	dirs := make([]string, 0, len(names))
	for i, name := range names {
		if name != filepath.Base(name) || strings.HasPrefix(name, ".") {
			return nil, fmt.Errorf("invalid layer name %q", name)
		}
		dir := filepath.Join(layerDir, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		// No nosuid: the image's setuid programs, such as sudo, keep working.
		if err := unix.Mount(filepath.Join(imageShareDir, name), dir, "erofs", unix.MS_RDONLY|unix.MS_NODEV, ""); err != nil {
			return nil, fmt.Errorf("mount image layer %s: %w", name, err)
		}
		dirs = append(dirs, dir)
	}
	return dirs, nil
}

// idmapBoot attaches the boot root idmapped so that the host user's files
// belong to root, and returns the idmapper for the shares.
func idmapBoot(m IDMap, agentUID, agentGID uint32) (*idmapper, error) {
	rootNS, sharesNS, err := idNamespaces(m, agentUID, agentGID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rootNS.Close() }()
	if err := attachIdmapped("/", bootLowerDir, rootNS); err != nil {
		_ = sharesNS.Close()
		return nil, fmt.Errorf("idmap /: %w", err)
	}
	return &idmapper{shares: sharesNS}, nil
}

// idmapLayers replaces each layer mount with an idmapped one that swaps the
// agent's IDs with the host user's, leaving all other IDs as they are.
func idmapLayers(layers []string, agentUID, agentGID, hostUID, hostGID uint32) error {
	if agentUID == hostUID && agentGID == hostGID {
		return nil
	}
	ns, err := userNamespace(swapIDs(agentUID, hostUID), swapIDs(agentGID, hostGID))
	if err != nil {
		return err
	}
	defer func() { _ = ns.Close() }()
	for _, dir := range layers {
		if err := attachIdmapped(dir, dir, ns); err != nil {
			return fmt.Errorf("idmap %s: %w", dir, err)
		}
	}
	return nil
}

// swapIDs returns ID mappings, in the form userNamespace takes, that
// exchange a and b and map every other valid ID to itself.
func swapIDs(a, b uint32) []syscall.SysProcIDMap {
	var maps []syscall.SysProcIDMap
	identity := func(from, to uint32) { // [from, to)
		if from < to {
			maps = append(maps, syscall.SysProcIDMap{ContainerID: int(from), HostID: int(from), Size: int(to - from)})
		}
	}
	if a == b {
		// The ID 2^32-1 is invalid and cannot be mapped.
		identity(0, math.MaxUint32)
		return maps
	}
	lo, hi := min(a, b), max(a, b)
	identity(0, lo)
	maps = append(maps, syscall.SysProcIDMap{ContainerID: int(lo), HostID: int(hi), Size: 1})
	identity(lo+1, hi)
	maps = append(maps, syscall.SysProcIDMap{ContainerID: int(hi), HostID: int(lo), Size: 1})
	identity(hi+1, math.MaxUint32)
	return maps
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
	opts := overlayOptions(lowers, upper, work)
	if len(opts) > maxMountData {
		return fmt.Errorf("overlay of %d layers exceeds the mount option limit", len(lowers))
	}
	if err := unix.Mount("overlay", target, "overlay", 0, opts); err != nil {
		return fmt.Errorf("mount overlay at %s: %w", target, err)
	}
	return nil
}

// overlayOptions returns overlayfs mount options for lowers, the first on
// top.
func overlayOptions(lowers []string, upper, work string) string {
	return "lowerdir=" + strings.Join(lowers, ":") + ",upperdir=" + upper + ",workdir=" + work
}
