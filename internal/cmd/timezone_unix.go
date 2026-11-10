//go:build darwin || linux

package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/seznam/jailoc/internal/guest"
)

// hostZoneinfoDirs are where hosts keep the time zone database, searched for
// a TZ zone name.
var hostZoneinfoDirs = []string{"/usr/share/zoneinfo", "/usr/lib/zoneinfo", "/usr/share/lib/zoneinfo", "/etc/zoneinfo"}

// workspaceTimeZone returns the time zone for the workspace VM: the
// configured zone name, or the host's local zone when name is empty. It
// returns nil when the host zone cannot be determined; the guest then keeps
// the image's zone.
func workspaceTimeZone(name string) *guest.TimeZone {
	if name != "" {
		return lookupTimeZone(name, true, "", "", hostZoneinfoDirs)
	}
	tz, set := os.LookupEnv("TZ")
	return lookupTimeZone(tz, set, "/etc/localtime", "/etc/timezone", hostZoneinfoDirs)
}

// lookupTimeZone resolves the local zone the way libc does: TZ when set,
// otherwise the localtime file. A TZ holding a POSIX rule rather than a zone
// name is not resolved.
func lookupTimeZone(tz string, tzSet bool, localtime, timezoneFile string, zoneinfoDirs []string) *guest.TimeZone {
	if tzSet {
		tz = strings.TrimPrefix(tz, ":")
		switch {
		case tz == "":
			tz = "UTC"
		case filepath.IsAbs(tz):
			return zoneFromFile(tz, zoneName(tz))
		}
		if !guest.ValidTimeZoneName(tz) {
			return nil
		}
		for _, dir := range zoneinfoDirs {
			if zone := zoneFromFile(filepath.Join(dir, tz), tz); zone != nil && zone.Data != nil {
				return zone
			}
		}
		return &guest.TimeZone{Name: tz}
	}
	name := ""
	fromLink := false
	if link, err := os.Readlink(localtime); err == nil {
		name, fromLink = zoneName(link), true
	} else if data, err := os.ReadFile(timezoneFile); err == nil { //nolint:gosec // G304: fixed system path
		if n := strings.TrimSpace(string(data)); guest.ValidTimeZoneName(n) {
			name = n
		}
	}
	zone := zoneFromFile(localtime, name)
	// /etc/timezone can go stale when localtime is a copy; the guest would
	// then prefer the wrong name over the right data.
	if zone != nil && !fromLink && zone.Name != "" && zone.Data != nil && !zoneMatches(zone, zoneinfoDirs) {
		zone.Name = ""
	}
	return zone
}

// zoneMatches reports whether a zoneinfo directory holds zone.Name with
// exactly zone.Data.
func zoneMatches(zone *guest.TimeZone, zoneinfoDirs []string) bool {
	for _, dir := range zoneinfoDirs {
		if z := zoneFromFile(filepath.Join(dir, zone.Name), zone.Name); z != nil && bytes.Equal(z.Data, zone.Data) {
			return true
		}
	}
	return false
}

// zoneFromFile returns the zone name with the TZif data at path, or nil when
// neither is known.
func zoneFromFile(path, name string) *guest.TimeZone {
	zone := &guest.TimeZone{Name: name}
	if f, err := os.Open(path); err == nil { //nolint:gosec // G304: host time zone file
		data, err := io.ReadAll(io.LimitReader(f, guest.MaxTimeZoneData+1))
		_ = f.Close()
		if err == nil && guest.IsTZif(data) {
			zone.Data = data
		}
	}
	if zone.Name == "" && zone.Data == nil {
		return nil
	}
	return zone
}

// zoneName extracts the zone name from a path into a zoneinfo directory,
// such as /var/db/timezone/zoneinfo/Europe/Prague on macOS or
// ../usr/share/zoneinfo/Europe/Prague on Linux.
func zoneName(path string) string {
	_, name, ok := strings.Cut(filepath.ToSlash(path), "zoneinfo/")
	if !ok {
		return ""
	}
	name = strings.TrimPrefix(name, "posix/")
	if !guest.ValidTimeZoneName(name) {
		return ""
	}
	return name
}
