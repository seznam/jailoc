package cmd

import (
	"testing"
)

func TestIsDuplicate(t *testing.T) {
	tests := []struct {
		name   string
		paths  []string
		target string
		want   bool
	}{
		{
			name:   "empty slice",
			paths:  []string{},
			target: "/path/to/something",
			want:   false,
		},
		{
			name:   "single element exact match",
			paths:  []string{"/home/user/projects"},
			target: "/home/user/projects",
			want:   true,
		},
		{
			name:   "single element no match",
			paths:  []string{"/home/user/projects"},
			target: "/home/user/other",
			want:   false,
		},
		{
			name:   "multiple elements match in middle",
			paths:  []string{"/path/a", "/path/b", "/path/c"},
			target: "/path/b",
			want:   true,
		},
		{
			name:   "multiple elements no match",
			paths:  []string{"/path/a", "/path/b", "/path/c"},
			target: "/path/d",
			want:   false,
		},
		{
			name:   "case sensitivity different",
			paths:  []string{"Foo"},
			target: "foo",
			want:   false,
		},
		{
			name:   "case sensitivity same",
			paths:  []string{"foo"},
			target: "foo",
			want:   true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isDuplicate(tc.paths, tc.target)
			if got != tc.want {
				t.Errorf("isDuplicate(%#v, %q) = %v, want %v", tc.paths, tc.target, got, tc.want)
			}
		})
	}
}
