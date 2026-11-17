package config

import "strings"

func MountsContainTarget(mounts []string, target string) bool {
	for _, spec := range mounts {
		mount, err := ParseMount(spec)
		if err == nil && mount.Container == target {
			return true
		}
	}
	return false
}

// ReadOnlyMountCoversPath returns the host source of the read-only mount in
// mounts that covers target and the host path target is backed by.
func ReadOnlyMountCoversPath(mounts []string, target string) (source, hostPath string, ok bool) {
	best := coveringMount(mounts, target)
	if best.Mode != "ro" {
		return "", "", false
	}
	return best.Host, best.Host + strings.TrimPrefix(target, best.Container), true
}

// MountHostPath returns the host path that target is backed by in mounts.
func MountHostPath(mounts []string, target string) (string, bool) {
	best := coveringMount(mounts, target)
	if best.Host == "" {
		return "", false
	}
	return best.Host + strings.TrimPrefix(target, best.Container), true
}

// coveringMount returns the mount in mounts with the longest container path
// that is target or one of its parents.
func coveringMount(mounts []string, target string) Mount {
	var best Mount
	for _, spec := range mounts {
		mount, err := ParseMount(spec)
		if err != nil || mount.Host == "" {
			continue
		}
		if mount.Container != target && !strings.HasPrefix(target, mount.Container+"/") {
			continue
		}
		if len(mount.Container) > len(best.Container) {
			best = mount
		}
	}
	return best
}
