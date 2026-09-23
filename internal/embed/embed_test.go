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
	appendAt := strings.Index(script, `cat "$CA_BUNDLE" >> "$SYSTEM_CA"`)
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
	appendAt := strings.Index(script, `cat "$CA_BUNDLE" >> "$SYSTEM_CA"`)
	privilegeDropAt := strings.Index(script, `exec su-exec rootless env HOME="$ROOTLESS_HOME"`)
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

func TestDindEntrypointFallsBackToFuseOverlayFSWhenNativeOverlayIsUnavailable(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	probeAt := strings.Index(script, "unshare -U -m -r")
	fuseDriverAt := strings.Index(script, `"storage-driver": "fuse-overlayfs"`)
	disableSnapshotterAt := strings.Index(script, `"containerd-snapshotter": false`)
	fallbackWriteAt := strings.Index(script, `if ! write_fallback_config; then`)
	privilegeDropAt := strings.Index(script, `exec su-exec rootless env HOME="$ROOTLESS_HOME"`)
	if probeAt < 0 || fuseDriverAt < 0 || disableSnapshotterAt < 0 || fallbackWriteAt < 0 || privilegeDropAt < 0 {
		t.Fatal("DinD entrypoint is missing the native overlay probe or fuse-overlayfs fallback")
	}
	if probeAt >= fallbackWriteAt || fallbackWriteAt >= privilegeDropAt {
		t.Fatal("DinD overlay compatibility fallback must be configured before privilege drop")
	}
	if !strings.Contains(script, "run_rootless") {
		t.Fatal("DinD entrypoint must run overlay probe as rootless user")
	}
}

func TestDindEntrypointProbesRootlessOverlayVariants(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	if strings.Count(script, "apk add --no-cache su-exec") != 1 {
		t.Fatal("DinD entrypoint must install su-exec exactly once")
	}
	if !strings.Contains(script, `for OVERLAY_OPTIONS in "userxattr," ""; do`) ||
		!strings.Contains(script, `-o ${OVERLAY_OPTIONS}lowerdir=`) {
		t.Fatal("DinD entrypoint must probe overlayfs with userxattr before the plain compatibility mount")
	}
}

func TestDindEntrypointReconcilesOnlyGeneratedFallbackConfig(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	markerAt := strings.Index(script, `FALLBACK_MARKER="/var/lib/jailoc/dind-overlay-fallback"`)
	createMarkerAt := strings.Index(script, `touch "$FALLBACK_MARKER"`)
	removeGeneratedAt := strings.Index(script, `rm -f "$DAEMON_CONFIG" "$FALLBACK_MARKER"`)
	preserveAt := strings.Index(script, `if [ -e "$DAEMON_CONFIG" ] && [ ! -f "$FALLBACK_MARKER" ]; then`)
	if markerAt < 0 || createMarkerAt < 0 || removeGeneratedAt < 0 || preserveAt < 0 {
		t.Fatal("DinD entrypoint must mark generated fallback config, reconcile it on success, and preserve user-managed config")
	}
	if strings.Count(script, `fallback_config | cmp -s - "$DAEMON_CONFIG"`) < 2 ||
		!strings.Contains(script, `rm -f "$FALLBACK_MARKER"`) {
		t.Fatal("DinD entrypoint must verify marked config before replacing or removing it")
	}
	if strings.Contains(script, `[ -f "$DAEMON_CONFIG" ] && [ ! -f "$FALLBACK_MARKER" ] && fallback_config | cmp`) {
		t.Fatal("DinD entrypoint must not infer config ownership from matching contents")
	}
	if strings.Contains(script, `chown 1000:1000 "$FALLBACK_MARKER"`) ||
		!strings.Contains(script, `if [ -L "$DAEMON_CONFIG" ]; then`) ||
		!strings.Contains(script, `if [ -e "$DAEMON_CONFIG" ] && ! fallback_config | cmp -s - "$DAEMON_CONFIG"; then`) {
		t.Fatal("DinD entrypoint must keep its marker root-owned, reject config symlinks, and allow a missing generated config to be recreated")
	}
	if !strings.Contains(script, `write_fallback_config()`) ||
		!strings.Contains(script, `mv -f "$TEMP_CONFIG" "$DAEMON_CONFIG"`) ||
		!strings.Contains(script, `if [ ! -e "$DAEMON_CONFIG" ]; then`) {
		t.Fatal("DinD entrypoint must atomically create fallback config and avoid rewriting matching config")
	}
	if !strings.Contains(script, `exit 2`) ||
		!strings.Contains(script, `if [ "$PROBE_STATUS" -eq 2 ]; then`) {
		t.Fatal("DinD entrypoint must distinguish probe cleanup failure from unsupported overlayfs")
	}
}
