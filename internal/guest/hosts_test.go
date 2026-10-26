package guest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAddHostsEntry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial *string
		want    string
	}{
		{name: "missing file", want: "127.0.1.1\tws\n"},
		{name: "appends", initial: new("127.0.0.1\tlocalhost\n"), want: "127.0.0.1\tlocalhost\n127.0.1.1\tws\n"},
		{name: "adds newline", initial: new("127.0.0.1 localhost"), want: "127.0.0.1 localhost\n127.0.1.1\tws\n"},
		{name: "already present", initial: new("10.0.0.5 other ws\n"), want: "10.0.0.5 other ws\n"},
		{name: "commented out", initial: new("# 127.0.1.1 ws\n"), want: "# 127.0.1.1 ws\n127.0.1.1\tws\n"},
		{name: "prefix is not a match", initial: new("127.0.1.1 ws2\n"), want: "127.0.1.1 ws2\n127.0.1.1\tws\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "hosts")
			if tt.initial != nil {
				if err := os.WriteFile(path, []byte(*tt.initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := addHostsEntry(path, "ws"); err != nil {
				t.Fatalf("addHostsEntry: %v", err)
			}
			got, err := os.ReadFile(path) //nolint:gosec // G304: test file
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Fatalf("hosts = %q, want %q", got, tt.want)
			}
		})
	}
}
