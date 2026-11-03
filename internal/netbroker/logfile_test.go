package netbroker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLogFileRotates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "broker.jsonl")
	l, err := openLogFile(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"aaaa\n", "bbbb\n", "cccc\n", "dddd\n"} {
		if _, err := l.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	cur, err := os.ReadFile(path) //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	prev, err := os.ReadFile(path + ".1") //nolint:gosec // G304: test file
	if err != nil {
		t.Fatal(err)
	}
	if string(cur) != "cccc\ndddd\n" || string(prev) != "aaaa\nbbbb\n" {
		t.Fatalf("log = %q, previous = %q", cur, prev)
	}
}

func TestFlowLimit(t *testing.T) {
	t.Parallel()
	l := flowLimit{max: 2}
	for range 2 {
		if !l.acquire() {
			t.Fatal("acquire below the cap failed")
		}
	}
	if l.acquire() {
		t.Fatal("acquire above the cap succeeded")
	}
	l.release()
	if !l.acquire() {
		t.Fatal("acquire after release failed")
	}
}
