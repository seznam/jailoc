package config

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseDetachKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		want    []byte
		wantErr string
	}{
		{in: DefaultDetachKeys, want: []byte{0x02, 'd'}},
		{in: `ctrl-\`, want: []byte{0x1c}},
		{in: "ctrl-a,ctrl-z,ctrl-@,ctrl-[,ctrl-],ctrl-^,ctrl-_", want: []byte{0x01, 0x1a, 0x00, 0x1b, 0x1d, 0x1e, 0x1f}},
		{in: "q,Q,1,@", want: []byte{'q', 'Q', '1', '@'}},
		{in: "", wantErr: "must not be empty"},
		{in: "ctrl-b,", wantErr: "detach_keys: "},
		{in: "ctrl-1", wantErr: "ctrl-1"},
		{in: "ab", wantErr: "ab"},
		{in: "ctrl-b, d", wantErr: " d"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := ParseDetachKeys(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseDetachKeys(%q) error = %v, want %q", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDetachKeys(%q): %v", tt.in, err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("ParseDetachKeys(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestValidateDetachKeys(t *testing.T) {
	t.Parallel()

	if err := Validate(&Config{DetachKeys: "ctrl-a,x"}); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	err := Validate(&Config{DetachKeys: "ctrl-"})
	if err == nil || !strings.Contains(err.Error(), "detach_keys: Unknown character") {
		t.Fatalf("Validate error = %v, want detach_keys error", err)
	}
}
