package embed_test

import (
	"strings"
	"testing"

	jailocembed "github.com/seznam/jailoc/internal/embed"
)

func TestDockerfileEmbedded(t *testing.T) {
	t.Parallel()
	if !strings.Contains(string(jailocembed.Dockerfile()), "FROM") {
		t.Fatal("Dockerfile() does not contain FROM")
	}
}

func TestDockerLayerEmbedded(t *testing.T) {
	t.Parallel()
	if !strings.Contains(string(jailocembed.DockerLayer()), "AS docker") {
		t.Fatal("DockerLayer() does not contain the docker stage")
	}
}

func TestTUIPluginEmbedded(t *testing.T) {
	t.Parallel()
	if len(jailocembed.TUIPluginJS()) == 0 || len(jailocembed.TUIPluginJSON()) == 0 {
		t.Fatal("TUI plugin assets are empty")
	}
}
