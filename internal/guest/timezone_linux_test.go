package guest

import (
	"os"
	"path/filepath"
	"testing"
)

var tzif = []byte("TZif2\x00\x00\x00 zone data")

func TestSetTimeZone(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		image      bool
		tz         TimeZone
		wantLink   string
		wantData   []byte
		wantZone   string
		wantErr    bool
		staleZone  bool
		noTimezone bool
	}{
		{name: "image zone", image: true, tz: TimeZone{Name: "Europe/Prague", Data: tzif}, wantLink: "/usr/share/zoneinfo/Europe/Prague", wantZone: "Europe/Prague\n"},
		{name: "host data when image lacks zone", tz: TimeZone{Name: "Europe/Prague", Data: tzif}, wantData: tzif, wantZone: "Europe/Prague\n"},
		{name: "host data without name", tz: TimeZone{Data: tzif}, wantData: tzif, staleZone: true, noTimezone: true},
		{name: "invalid name uses data", image: true, tz: TimeZone{Name: "../etc/passwd", Data: tzif}, wantData: tzif, noTimezone: true},
		{name: "no zone", tz: TimeZone{Name: "Europe/Prague"}, wantErr: true},
		{name: "invalid data", tz: TimeZone{Data: []byte("not a zone")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.image {
				dir := filepath.Join(root, ZoneinfoDir, "Europe")
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "Prague"), tzif, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			etc := filepath.Join(root, "etc")
			if err := os.MkdirAll(etc, 0o750); err != nil {
				t.Fatal(err)
			}
			// The image default the zone replaces.
			if err := os.Symlink("/usr/share/zoneinfo/Etc/UTC", filepath.Join(etc, "localtime")); err != nil {
				t.Fatal(err)
			}
			if tt.staleZone {
				if err := os.WriteFile(filepath.Join(etc, "timezone"), []byte("Etc/UTC\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			err := setTimeZone(root, tt.tz)
			if tt.wantErr {
				if err == nil {
					t.Fatal("want error")
				}
				if link, _ := os.Readlink(filepath.Join(etc, "localtime")); link != "/usr/share/zoneinfo/Etc/UTC" {
					t.Fatalf("localtime changed on error: %q", link)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			localtime := filepath.Join(etc, "localtime")
			if tt.wantLink != "" {
				if link, err := os.Readlink(localtime); err != nil || link != tt.wantLink {
					t.Fatalf("localtime link = %q, %v; want %q", link, err, tt.wantLink)
				}
			} else {
				info, err := os.Lstat(localtime)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("localtime not a regular file: %v", err)
				}
				got, _ := os.ReadFile(localtime) //nolint:gosec // G304: test temp dir
				if string(got) != string(tt.wantData) {
					t.Fatalf("localtime = %q, want %q", got, tt.wantData)
				}
			}
			got, err := os.ReadFile(filepath.Join(etc, "timezone")) //nolint:gosec // G304: test temp dir
			if tt.noTimezone {
				if !os.IsNotExist(err) {
					t.Fatalf("/etc/timezone = %q, %v; want none", got, err)
				}
				return
			}
			if string(got) != tt.wantZone {
				t.Fatalf("/etc/timezone = %q, want %q", got, tt.wantZone)
			}
		})
	}
}
