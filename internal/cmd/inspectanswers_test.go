package cmd

import (
	"strings"
	"testing"
)

func TestCollapseAnswers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		answers []string
		want    string
	}{
		{"empty", nil, ""},
		{"single", []string{"1.2.3.4"}, "1.2.3.4"},
		{"sorted and deduplicated", []string{"9.9.9.9", "1.1.1.1", "9.9.9.9"}, "1.1.1.1, 9.9.9.9"},
		{"third octet pattern", []string{"104.16.2.34", "104.16.0.34", "104.16.1.34"}, "104.16.{0-2}.34"},
		{"last octet pattern with gaps", []string{"10.0.0.9", "10.0.0.1", "10.0.0.3", "10.0.0.5", "10.0.0.7", "10.0.0.8", "10.0.0.6"}, "10.0.0.{1,3,5-9}"},
		{"pair is listed in the pattern", []string{"1.2.3.4", "1.2.3.6"}, "1.2.3.{4,6}"},
		{"unaligned run is a pattern", []string{"1.2.3.5", "1.2.3.6"}, "1.2.3.{5,6}"},
		{"aligned block is a prefix", []string{"1.2.3.7", "1.2.3.4", "1.2.3.5", "1.2.3.6"}, "1.2.3.4/30"},
		{"aligned pair is a prefix", []string{"1.2.3.5", "1.2.3.4"}, "1.2.3.4/31"},
		{"prefix and pattern sorted", []string{"5.5.5.5", "1.2.3.1", "1.2.3.0", "5.5.6.5"}, "1.2.3.0/31, 5.5.{5,6}.5"},
		{"last octet first", []string{"1.2.3.4", "1.2.3.9", "1.2.4.4"}, "1.2.3.{4,9}, 1.2.4.4"},
		{"other octets unrelated", []string{"1.2.3.4", "5.6.7.8"}, "1.2.3.4, 5.6.7.8"},
		{"ipv6 listed", []string{"2606:4700::6810:2", "2606:4700::6810:1"}, "2606:4700::6810:1, 2606:4700::6810:2"},
		{"ipv6 aligned block", []string{"2001:db8::3", "2001:db8::2"}, "2001:db8::2/127"},
		{"ipv4 before ipv6", []string{"2001:db8::1", "1.1.1.1"}, "1.1.1.1, 2001:db8::1"},
		{"non-address kept last", []string{"cdn.example.", "1.1.1.1"}, "1.1.1.1, cdn.example."},
		{"non-address escaped", []string{"a\x1bb"}, `a\x1bb`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := strings.Join(collapseAnswers(tt.answers), ", "); got != tt.want {
				t.Fatalf("collapseAnswers(%q) = %q, want %q", tt.answers, got, tt.want)
			}
		})
	}
}
