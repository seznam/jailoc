package workspace_test

import (
	"os"
	"testing"

	"github.com/seznam/jailoc/internal/config"
)

// TestMain drops a developer's JAILOC_CONFIG so tests that isolate HOME
// never touch the real configuration file.
func TestMain(m *testing.M) {
	_ = os.Unsetenv(config.PathEnv) // cannot fail for a valid name
	os.Exit(m.Run())
}
