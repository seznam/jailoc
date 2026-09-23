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

func TestFilteredDNSSidecarReachableThroughFirewall(t *testing.T) {
	t.Parallel()
	for name, script := range map[string]string{
		"opencode": string(jailocembed.Entrypoint()),
		"dind":     string(jailocembed.DindEntrypoint()),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			for _, proto := range []string{"udp", "tcp"} {
				rejectAt := strings.Index(script, "-p "+proto+" --dport 53 -j REJECT")
				allowAt := strings.Index(script, "-p "+proto+" -d 169.254.53.53 --dport 53 -j ACCEPT")
				if rejectAt < 0 || allowAt <= rejectAt {
					t.Errorf("%s: sidecar DNS allow rule must be inserted after catch-all reject so it takes precedence", proto)
				}
			}
		})
	}
}
