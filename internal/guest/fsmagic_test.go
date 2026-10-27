package guest

import (
	"bytes"
	"testing"
)

func TestExtMagicAt(t *testing.T) {
	t.Parallel()
	ext := make([]byte, 2048)
	ext[1080], ext[1081] = 0x53, 0xEF
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{name: "ext superblock", data: ext, want: true},
		{name: "blank disk", data: make([]byte, 2048), want: false},
		{name: "shorter than superblock", data: make([]byte, 100), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := extMagicAt(bytes.NewReader(tt.data))
			if err != nil {
				t.Fatalf("extMagicAt: %v", err)
			}
			if got != tt.want {
				t.Fatalf("extMagicAt = %v, want %v", got, tt.want)
			}
		})
	}
}
