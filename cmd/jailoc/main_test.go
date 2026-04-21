package main

import "testing"

func TestPseudoVersionRevision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "release branch pseudo version",
			version: "v1.11.1-0.20260421130442-3758b6c5e57a",
			want:    "3758b6c5e57a",
		},
		{
			name:    "prerelease pseudo version",
			version: "v1.11.1-rc.1.0.20260421130442-3758b6c5e57a",
			want:    "3758b6c5e57a",
		},
		{
			name:    "invalid revision length",
			version: "v1.11.1-0.20260421130442-deadbeef",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pseudoVersionRevision(tt.version)
			if got != tt.want {
				t.Fatalf("pseudoVersionRevision(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}

func TestPseudoVersionTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		want    string
	}{
		{
			name:    "release branch pseudo version",
			version: "v1.11.1-0.20260421130442-3758b6c5e57a",
			want:    "2026-04-21T13:04:42Z",
		},
		{
			name:    "prerelease pseudo version",
			version: "v1.11.1-rc.1.0.20260421130442-3758b6c5e57a",
			want:    "2026-04-21T13:04:42Z",
		},
		{
			name:    "invalid timestamp",
			version: "v1.11.1-0.notatimestamp-3758b6c5e57a",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := pseudoVersionTime(tt.version)
			if got != tt.want {
				t.Fatalf("pseudoVersionTime(%q) = %q, want %q", tt.version, got, tt.want)
			}
		})
	}
}
