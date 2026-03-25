package embed_test

import (
	"strings"
	"testing"

	jailocembed "github.com/seznam/jailoc/internal/embed"
)

func TestDockerfileEmbedded(t *testing.T) {
	b := jailocembed.Dockerfile()
	if len(b) == 0 {
		t.Fatal("Dockerfile() returned empty bytes")
	}
	if !strings.Contains(string(b), "FROM") {
		t.Fatal("Dockerfile() does not contain FROM")
	}
}

func TestComposeTemplateEmbedded(t *testing.T) {
	s := jailocembed.ComposeTemplate()
	if s == "" {
		t.Fatal("ComposeTemplate() returned empty string")
	}
	if !strings.Contains(s, "services:") {
		t.Fatal("ComposeTemplate() does not contain 'services:'")
	}
}

func TestEntrypointEmbedded(t *testing.T) {
	b := jailocembed.Entrypoint()
	if len(b) == 0 {
		t.Fatal("Entrypoint() returned empty bytes")
	}
	if !strings.Contains(string(b), "#!/bin/bash") {
		t.Fatal("Entrypoint() does not contain #!/bin/bash")
	}
}
