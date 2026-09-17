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

func TestEntrypointInstallsCABundleBeforePrivilegeDrop(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.Entrypoint())

	pathAt := strings.Index(script, `CA_BUNDLE="/etc/jailoc/ca-bundle.pem"`)
	appendAt := strings.Index(script, `cat "$CA_BUNDLE" >> /etc/ssl/certs/ca-certificates.crt`)
	nodeTrustAt := strings.Index(script, `NODE_USE_SYSTEM_CA=1`)
	privilegeDropAt := strings.Index(script, "exec setpriv")
	if pathAt < 0 || appendAt < 0 || nodeTrustAt < 0 || privilegeDropAt < 0 {
		t.Fatal("opencode entrypoint is missing CA installation, Node trust, or privilege drop")
	}
	if pathAt >= appendAt || appendAt >= nodeTrustAt || nodeTrustAt >= privilegeDropAt {
		t.Fatal("opencode CA installation and Node trust must occur before privilege drop")
	}
}

func TestEntrypointSkipsAbsentAndRejectsInvalidCABundle(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.Entrypoint())

	if !strings.Contains(script, `if [ -e "$CA_BUNDLE" ]; then`) {
		t.Fatal("opencode entrypoint must inspect any present CA bundle path")
	}
	if !strings.Contains(script, `[ ! -f "$CA_BUNDLE" ] || [ ! -s "$CA_BUNDLE" ]`) {
		t.Fatal("opencode entrypoint must reject non-regular or empty CA bundles")
	}
	if !strings.Contains(script, "jailoc: FATAL:") {
		t.Fatal("opencode entrypoint must report invalid CA bundles as fatal")
	}
}

func TestDindEntrypointEmbedded(t *testing.T) {
	t.Parallel()

	b := jailocembed.DindEntrypoint()
	if len(b) == 0 {
		t.Fatal("DindEntrypoint() returned empty bytes")
	}
	if !strings.HasPrefix(string(b), "#!/bin/sh\n") {
		t.Fatal("DindEntrypoint() does not contain #!/bin/sh")
	}
}

func TestDindEntrypointInstallsCABundleBeforePrivilegeDrop(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	pathAt := strings.Index(script, `CA_BUNDLE="/etc/jailoc/ca-bundle.pem"`)
	certOwnershipAt := strings.Index(script, "for d in /certs/ca /certs/client; do")
	appendAt := strings.Index(script, `cat "$CA_BUNDLE" >> /etc/ssl/certs/ca-certificates.crt`)
	privilegeDropAt := strings.Index(script, "exec su-exec rootless")
	if pathAt < 0 || certOwnershipAt < 0 || appendAt < 0 || privilegeDropAt < 0 {
		t.Fatalf("DinD entrypoint is missing CA installation or existing ownership/privilege-drop steps")
	}
	if certOwnershipAt >= appendAt || appendAt >= privilegeDropAt {
		t.Fatalf("DinD CA append must occur after TLS ownership and before privilege drop")
	}
}

func TestDindEntrypointSkipsAbsentAndRejectsInvalidCABundle(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	if !strings.Contains(script, `if [ -e "$CA_BUNDLE" ]; then`) {
		t.Fatal("DinD entrypoint must inspect any present CA bundle path")
	}
	if !strings.Contains(script, `[ ! -f "$CA_BUNDLE" ] || [ ! -s "$CA_BUNDLE" ]`) {
		t.Fatal("DinD entrypoint must reject non-regular or empty CA bundles")
	}
	if !strings.Contains(script, "jailoc-dind: FATAL:") {
		t.Fatal("DinD entrypoint must report invalid CA bundles as fatal")
	}
}

func TestDindEntrypointDoesNotUseUpdateCACertificates(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	if strings.Contains(script, "update-ca-certificates") {
		t.Fatal("DinD entrypoint must append multi-certificate bundles directly")
	}
}
