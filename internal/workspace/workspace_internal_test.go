package workspace

import "testing"

// Helper functions for creating pointers in tests.
func f64(v float64) *float64 { return &v }
func str(v string) *string   { return &v }

func TestPathSegments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want int
	}{
		{"/a/b/c", 3},
		{"/a/bb", 2},
		{"/a", 1},
		{"/", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			t.Parallel()
			if got := pathSegments(tt.path); got != tt.want {
				t.Fatalf("pathSegments(%q) = %d, want %d", tt.path, got, tt.want)
			}
		})
	}
}

func TestFloatWithDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		defaultVal *float64
		override   *float64
		fallback   float64
		want       float64
	}{
		{
			name:       "override set returns override",
			defaultVal: f64(1.0),
			override:   f64(2.0),
			fallback:   3.0,
			want:       2.0,
		},
		{
			name:       "override nil and defaultVal set returns defaultVal",
			defaultVal: f64(1.0),
			override:   nil,
			fallback:   3.0,
			want:       1.0,
		},
		{
			name:       "both nil returns fallback",
			defaultVal: nil,
			override:   nil,
			fallback:   3.0,
			want:       3.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := floatWithDefault(tt.defaultVal, tt.override, tt.fallback)
			if got != tt.want {
				t.Fatalf("floatWithDefault(%v, %v, %v) = %v, want %v",
					tt.defaultVal, tt.override, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestStringWithDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		defaultVal *string
		override   *string
		fallback   string
		want       string
	}{
		{
			name:       "override set returns override",
			defaultVal: str("default"),
			override:   str("override"),
			fallback:   "fallback",
			want:       "override",
		},
		{
			name:       "override nil and defaultVal set returns defaultVal",
			defaultVal: str("default"),
			override:   nil,
			fallback:   "fallback",
			want:       "default",
		},
		{
			name:       "both nil returns fallback",
			defaultVal: nil,
			override:   nil,
			fallback:   "fallback",
			want:       "fallback",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := stringWithDefault(tt.defaultVal, tt.override, tt.fallback)
			if got != tt.want {
				t.Fatalf("stringWithDefault(%v, %v, %q) = %q, want %q",
					tt.defaultVal, tt.override, tt.fallback, got, tt.want)
			}
		})
	}
}
