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

func ReadOnlyMountCoversPath(mounts []string, target string) (string, bool) {
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
	if best.Mode != "ro" {
		return "", false
	}
	return best.Host + strings.TrimPrefix(target, best.Container), true
}
