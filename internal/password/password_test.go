package password

import (
	"path/filepath"
	"regexp"
	"testing"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	first, err := Generate()
	if err != nil {
		t.Fatalf("Generate() first call error: %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("Generate() second call error: %v", err)
	}

	re := regexp.MustCompile(`^[0-9a-f]{64}$`)
	if !re.MatchString(first) {
		t.Fatalf("first password has invalid format: %q", first)
	}
	if !re.MatchString(second) {
		t.Fatalf("second password has invalid format: %q", second)
	}
	if first == second {
		t.Fatal("expected two generated passwords to differ")
	}
}

func TestDataDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := DataDir("my-workspace")
	want := filepath.Join(home, ".local", "share", "jailoc", "my-workspace")

	if got != want {
		t.Fatalf("DataDir() = %q, want %q", got, want)
	}
}

func TestPasswordFilePath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got := PasswordFilePath("my-workspace")
	want := filepath.Join(home, ".local", "share", "jailoc", "my-workspace", "password")

	if got != want {
		t.Fatalf("PasswordFilePath() = %q, want %q", got, want)
	}
}
