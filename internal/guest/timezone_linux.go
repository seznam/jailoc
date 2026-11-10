package guest

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// setTimeZone makes tz the local time zone of the system under root: it
// links /etc/localtime to the image's zoneinfo file for tz.Name, or installs
// tz.Data when the image lacks that zone, and records the name in
// /etc/timezone.
func setTimeZone(root string, tz TimeZone) error {
	named := ValidTimeZoneName(tz.Name) && imageHasZone(root, tz.Name)
	if !named && !IsTZif(tz.Data) {
		return fmt.Errorf("time zone %q: not in %s and no zone data from the host", tz.Name, ZoneinfoDir)
	}
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil { //nolint:gosec // G301: system directory
		return fmt.Errorf("create %s: %w", etc, err)
	}
	localtime := filepath.Join(etc, "localtime")
	tmp := localtime + ".jailoc"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", tmp, err)
	}
	if named {
		if err := os.Symlink(filepath.Join(ZoneinfoDir, tz.Name), tmp); err != nil {
			return fmt.Errorf("link time zone: %w", err)
		}
	} else if err := os.WriteFile(tmp, tz.Data, 0o644); err != nil { //nolint:gosec // G306: public zone data
		return fmt.Errorf("write time zone: %w", err)
	}
	if err := os.Rename(tmp, localtime); err != nil {
		return fmt.Errorf("install %s: %w", localtime, err)
	}
	timezone := filepath.Join(etc, "timezone")
	if !ValidTimeZoneName(tz.Name) {
		if err := os.Remove(timezone); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", timezone, err)
		}
		return nil
	}
	if err := os.WriteFile(timezone, []byte(tz.Name+"\n"), 0o644); err != nil { //nolint:gosec // G306: public zone name
		return fmt.Errorf("write %s: %w", timezone, err)
	}
	return nil
}

// imageHasZone reports whether the image under root has a TZif file for name.
func imageHasZone(root, name string) bool {
	f, err := os.Open(filepath.Join(root, ZoneinfoDir, name)) //nolint:gosec // G304: name checked by ValidTimeZoneName
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 4)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return IsTZif(head)
}

// timeZoneName names tz for logs.
func timeZoneName(tz *TimeZone) string {
	switch {
	case tz == nil:
		return ""
	case tz.Name == "":
		return "host zone data"
	}
	return tz.Name
}
