package guest

import (
	"bytes"
	"io/fs"
)

// TimeZone is the host's local time zone. The guest points /etc/localtime
// at the image's zoneinfo file for Name and installs Data when the image
// has none, so images without tzdata follow the host zone too.
type TimeZone struct {
	// Name is the IANA zone name, such as Europe/Prague, when the host
	// knows it.
	Name string `json:"name,omitempty"`
	// Data is the host's TZif file for the zone.
	Data []byte `json:"data,omitempty"`
}

// ZoneinfoDir is the time zone database in the guest image.
const ZoneinfoDir = "/usr/share/zoneinfo"

// MaxTimeZoneData bounds the TZif data the host sends; real zone files are
// a few KiB.
const MaxTimeZoneData = 64 << 10

// ValidTimeZoneName reports whether name is an IANA-style zone name that
// stays inside ZoneinfoDir.
func ValidTimeZoneName(name string) bool {
	if name == "" || !fs.ValidPath(name) {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '/', r == '_', r == '-', r == '+':
		default:
			return false
		}
	}
	return true
}

// IsTZif reports whether data looks like a TZif time zone file.
func IsTZif(data []byte) bool {
	return len(data) <= MaxTimeZoneData && bytes.HasPrefix(data, []byte("TZif"))
}
