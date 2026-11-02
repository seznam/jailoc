package config

import (
	"os"
	"testing"
)

// TestMain drops a developer's JAILOC_CONFIG so tests that isolate HOME
// never touch the real configuration file.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(PathEnv) // cannot fail for a valid name
	os.Exit(m.Run())
}
