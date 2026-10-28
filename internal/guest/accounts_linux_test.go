package guest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteEntries(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "passwd")
	in := "root:x:0:0:root:/root:/bin/bash\nagent:x:1000:1000::/home/agent:/bin/bash\n"
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	err := rewriteEntries(path, func(f []string) bool {
		if f[0] != "agent" {
			return false
		}
		f[2], f[3] = "1001", "118"
		return true
	})
	if err != nil {
		t.Fatalf("rewriteEntries: %v", err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(in, "agent:x:1000:1000:", "agent:x:1001:118:", 1)
	if string(got) != want {
		t.Fatalf("passwd = %q, want %q", got, want)
	}
	if err := rewriteEntries(filepath.Join(t.TempDir(), "missing"), func([]string) bool { return true }); err != nil {
		t.Fatalf("missing file: %v", err)
	}
}
