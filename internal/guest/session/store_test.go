package session

import (
	"os"
	"testing"
	"time"
)

func TestListPreservesStartupLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const id = "abcdef"
	if err := writeInfo(dir, &Info{ID: id, Created: time.Now().Add(-2 * startTimeout)}); err != nil {
		t.Fatal(err)
	}
	const diagnostic = "jailoc-guest: start agent: no such file or directory\n"
	if err := os.WriteFile(logPath(dir, id), []byte(diagnostic), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := List(dir)
	if err != nil || len(infos) != 0 {
		t.Fatalf("List = %v, %v; want no sessions", infos, err)
	}
	if _, err := os.Stat(infoPath(dir, id)); !os.IsNotExist(err) {
		t.Fatalf("stale metadata not removed: %v", err)
	}
	data, err := os.ReadFile(logPath(dir, id)) //nolint:gosec // G304: diagnostic created inside t.TempDir
	if err != nil || string(data) != diagnostic {
		t.Fatalf("startup diagnostic = %q, %v; want %q", data, err, diagnostic)
	}
}
