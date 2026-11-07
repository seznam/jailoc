//go:build darwin || linux

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestZoneName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "linux relative link", path: "../usr/share/zoneinfo/Europe/Prague", want: "Europe/Prague"},
		{name: "macOS link", path: "/var/db/timezone/zoneinfo/America/New_York", want: "America/New_York"},
		{name: "posix subtree", path: "/usr/share/zoneinfo/posix/Asia/Tokyo", want: "Asia/Tokyo"},
		{name: "single component", path: "/usr/share/zoneinfo/UTC", want: "UTC"},
		{name: "no zoneinfo dir", path: "/etc/mytime", want: ""},
		{name: "traversal", path: "/usr/share/zoneinfo/../../etc/passwd", want: ""},
		{name: "empty name", path: "/usr/share/zoneinfo/", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := zoneName(tt.path); got != tt.want {
				t.Fatalf("zoneName(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestLookupTimeZone(t *testing.T) {
	t.Parallel()

	tzif := []byte("TZif2\x00\x00\x00 fake zone data")
	writeFile := func(t *testing.T, path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name     string
		tz       string
		tzSet    bool
		setup    func(t *testing.T, dir string)
		wantNil  bool
		wantName string
		wantData bool
	}{
		{
			name: "localtime symlink into zoneinfo",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "zoneinfo", "Europe", "Prague"), tzif)
				if err := os.Symlink(filepath.Join(dir, "zoneinfo", "Europe", "Prague"), filepath.Join(dir, "localtime")); err != nil {
					t.Fatal(err)
				}
			},
			wantName: "Europe/Prague",
			wantData: true,
		},
		{
			name: "localtime copy with timezone file",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "localtime"), tzif)
				writeFile(t, filepath.Join(dir, "timezone"), []byte("Europe/Berlin\n"))
				writeFile(t, filepath.Join(dir, "zoneinfo", "Europe", "Berlin"), tzif)
			},
			wantName: "Europe/Berlin",
			wantData: true,
		},
		{
			name: "localtime copy with stale timezone file",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "localtime"), tzif)
				writeFile(t, filepath.Join(dir, "timezone"), []byte("Europe/Berlin\n"))
				writeFile(t, filepath.Join(dir, "zoneinfo", "Europe", "Berlin"), []byte("TZif2 other zone"))
			},
			wantData: true,
		},
		{
			name: "localtime copy with timezone file missing on host",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "localtime"), tzif)
				writeFile(t, filepath.Join(dir, "timezone"), []byte("Europe/Berlin\n"))
			},
			wantData: true,
		},
		{
			name: "localtime copy without name",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "localtime"), tzif)
			},
			wantData: true,
		},
		{
			name:    "nothing known",
			setup:   func(*testing.T, string) {},
			wantNil: true,
		},
		{
			name: "localtime not TZif",
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "localtime"), []byte("garbage"))
			},
			wantNil: true,
		},
		{
			name:  "TZ zone name found in zoneinfo",
			tz:    ":Asia/Tokyo",
			tzSet: true,
			setup: func(t *testing.T, dir string) {
				writeFile(t, filepath.Join(dir, "zoneinfo", "Asia", "Tokyo"), tzif)
			},
			wantName: "Asia/Tokyo",
			wantData: true,
		},
		{
			name:     "TZ zone name missing on host",
			tz:       "America/Chicago",
			tzSet:    true,
			setup:    func(*testing.T, string) {},
			wantName: "America/Chicago",
		},
		{
			name:     "TZ empty means UTC",
			tz:       "",
			tzSet:    true,
			setup:    func(*testing.T, string) {},
			wantName: "UTC",
		},
		{
			name:    "TZ POSIX rule",
			tz:      "CET-1CEST,M3.5.0,M10.5.0/3",
			tzSet:   true,
			setup:   func(*testing.T, string) {},
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			tt.setup(t, dir)
			got := lookupTimeZone(tt.tz, tt.tzSet, filepath.Join(dir, "localtime"), filepath.Join(dir, "timezone"), []string{filepath.Join(dir, "zoneinfo")})
			if tt.wantNil {
				if got != nil {
					t.Fatalf("lookupTimeZone() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("lookupTimeZone() = nil")
			}
			if got.Name != tt.wantName {
				t.Errorf("Name = %q, want %q", got.Name, tt.wantName)
			}
			if tt.wantData != (got.Data != nil) {
				t.Errorf("Data present = %v, want %v", got.Data != nil, tt.wantData)
			}
			if tt.wantData && !bytes.Equal(got.Data, tzif) {
				t.Errorf("Data = %q, want %q", got.Data, tzif)
			}
		})
	}
}

func TestLookupTimeZoneAbsoluteTZ(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "zoneinfo", "Europe", "Lisbon")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("TZif2 data"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := lookupTimeZone(path, true, "/nonexistent", "/nonexistent", nil)
	if got == nil || got.Name != "Europe/Lisbon" || got.Data == nil {
		t.Fatalf("lookupTimeZone(%q) = %+v, want Europe/Lisbon with data", path, got)
	}
}

func TestWorkspaceTimeZoneConfigured(t *testing.T) {
	t.Parallel()

	got := workspaceTimeZone("Etc/UTC")
	if got == nil || got.Name != "Etc/UTC" {
		t.Fatalf("workspaceTimeZone(%q) = %+v, want Etc/UTC", "Etc/UTC", got)
	}
}
