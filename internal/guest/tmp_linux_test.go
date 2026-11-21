//go:build linux

package guest

import (
	"os"
	"strings"
	"testing"
)

// TestSessionEnvTmp cannot be parallel: it sets the process environment
// that sessionEnv reads as the image's.
func TestSessionEnvTmp(t *testing.T) {
	for _, name := range tmpVars {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TMPDIR", "/image")
	env, _ := sessionEnv(&Config{Env: []string{"TEMP=/configured"}})
	got := map[string][]string{}
	for _, kv := range env {
		name, value, _ := strings.Cut(kv, "=")
		got[name] = append(got[name], value)
	}
	want := map[string]string{"TMPDIR": "/image", "TEMP": "/configured", "TMP": "/tmp", "GOTMPDIR": "/tmp"}
	for name, value := range want {
		if len(got[name]) != 1 || got[name][0] != value {
			t.Errorf("%s = %q, want only %q", name, got[name], value)
		}
	}
}
