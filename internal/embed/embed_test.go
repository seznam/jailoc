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
	restoreAt := strings.Index(script, `if [ -f "$ORIGINAL_CA" ] && [ ! -e "$CA_BUNDLE" ]; then`)
	installAt := strings.Index(script, "apk add --no-cache su-exec")
	probeAt := strings.Index(script, "if run_rootless '")
	privilegeDropAt := strings.Index(script, `exec su-exec rootless env HOME="$ROOTLESS_HOME"`)
	if pathAt < 0 || certOwnershipAt < 0 || appendAt < 0 || restoreAt < 0 || installAt < 0 || probeAt < 0 || privilegeDropAt < 0 {
		t.Fatalf("DinD entrypoint is missing CA installation or existing ownership/privilege-drop steps")
	}
	if certOwnershipAt >= pathAt || pathAt >= appendAt || appendAt >= restoreAt || restoreAt >= installAt || installAt >= probeAt || probeAt >= privilegeDropAt {
		t.Fatalf("DinD entrypoint must fix cert ownership, install or restore the CA bundle, install su-exec, probe overlayfs, then drop privileges")
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
	if !strings.Contains(script, `if ! command -v su-exec >/dev/null 2>&1; then`) ||
		!strings.Contains(script, "jailoc-dind: FATAL: could not install su-exec") {
		t.Fatal("DinD entrypoint must install su-exec only when absent and fail with diagnostic on error")
	}
	if !strings.Contains(script, `su-exec rootless env HOME="$ROOTLESS_HOME" sh -c "$1"`) ||
		!strings.Contains(script, `su rootless -s /bin/sh -c "HOME='$ROOTLESS_HOME' $1"`) &&
			!strings.Contains(script, `HOME="$ROOTLESS_HOME" su rootless -s /bin/sh -c "$1"`) &&
			!strings.Contains(script, `env HOME="$ROOTLESS_HOME" su rootless -s /bin/sh -c "$1"`) {
		t.Fatal("DinD entrypoint must set rootless HOME in both su-exec and su branches of run_rootless")
	}
}

func TestDindEntrypointReconcilesOnlyGeneratedFallbackConfig(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	if !strings.Contains(script, `FALLBACK_MARKER="/var/lib/jailoc/dind-overlay-fallback"`) ||
		!strings.Contains(script, `touch "$FALLBACK_MARKER"`) ||
		!strings.Contains(script, `[ ! -f "$FALLBACK_MARKER" ] || ! managed_config | cmp -s - "$DAEMON_CONFIG"`) {
		t.Fatal("DinD entrypoint must track container-local generated config ownership and refuse unmarked config")
	}
	nonRegularGuardAt := strings.Index(script, `[ -e "$DAEMON_CONFIG" ] && [ ! -f "$DAEMON_CONFIG" ]`)
	firstCompareAt := strings.Index(script, `cmp -s - "$DAEMON_CONFIG"`)
	if nonRegularGuardAt < 0 || firstCompareAt < 0 || nonRegularGuardAt >= firstCompareAt {
		t.Fatal("DinD entrypoint must reject existing non-regular daemon config before comparing its contents")
	}
	if strings.Contains(script, `chown 1000:1000 "$FALLBACK_MARKER"`) ||
		!strings.Contains(script, `if [ -L "$DAEMON_CONFIG" ]; then`) {
		t.Fatal("DinD entrypoint must keep its marker root-owned, reject config symlinks, and allow a missing generated config to be recreated")
	}
	if !strings.Contains(script, `write_fallback_config()`) ||
		!strings.Contains(script, `mv -f "$TEMP_CONFIG" "$DAEMON_CONFIG"`) {
		t.Fatal("DinD entrypoint must atomically create fallback config and avoid rewriting matching config")
	}
	if !strings.Contains(script, `exit 2`) ||
		!strings.Contains(script, `if [ "$PROBE_STATUS" -eq 2 ]; then`) {
		t.Fatal("DinD entrypoint must distinguish probe cleanup failure from unsupported overlayfs")
	}
}
func TestDindEntrypointDurableBackendSelection(t *testing.T) {
	t.Parallel()
	script := string(jailocembed.DindEntrypoint())

	if !strings.Contains(script, `DOCKER_DATA_DIR="$ROOTLESS_HOME/.local/share/docker"`) ||
		!strings.Contains(script, `BACKEND_DIR="/var/lib/jailoc/storage"`) ||
		!strings.Contains(script, `BACKEND_MARKER="$BACKEND_DIR/storage-mode"`) {
		t.Fatal("DinD entrypoint must define persistent storage backend marker outside rootless-writable Docker data")
	}
	if !strings.Contains(script, "native-snapshotter") || !strings.Contains(script, "fuse-overlayfs") {
		t.Fatal("DinD entrypoint must support native-snapshotter and fuse-overlayfs backend marker values")
	}
	if !strings.Contains(script, `"containerd-snapshotter": true`) {
		t.Fatal("DinD entrypoint must explicitly pin containerd-snapshotter true in managed native config to prevent default drift")
	}
	if !strings.Contains(script, `"containerd-snapshotter": false`) ||
		!strings.Contains(script, `"storage-driver": "fuse-overlayfs"`) {
		t.Fatal("DinD entrypoint must pin fuse driver and containerd-snapshotter false in managed fallback config")
	}
	if !strings.Contains(script, `mv -f "$TEMP_MARKER" "$BACKEND_MARKER"`) {
		t.Fatal("DinD entrypoint must atomically rename temporary marker on same volume")
	}
	if !strings.Contains(script, `touch "$FALLBACK_MARKER"`) ||
		strings.Index(script, `touch "$FALLBACK_MARKER"`) > strings.Index(script, `if ! write_fallback_config; then`) {
		t.Fatal("DinD entrypoint must record generated config ownership before publishing config")
	}
	if !strings.Contains(script, `"$BACKEND" = native-snapshotter`) ||
		!strings.Contains(script, `[ "$PROBE_STATUS" -ne 0 ]`) {
		t.Fatal("DinD entrypoint must refuse switching to fuse when volume has native marker and probe fails")
	}
	if !strings.Contains(script, `find "$DOCKER_DATA_DIR" -mindepth 1 -print -quit`) {
		t.Fatal("DinD entrypoint must fail closed for unmarked Docker data, including directory-only volumes")
	}
}
