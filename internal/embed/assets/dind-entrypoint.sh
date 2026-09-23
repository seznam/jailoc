#!/bin/sh
set -eu

# Network isolation for the rootless DinD sidecar.
#
# The entrypoint runs as root to install iptables rules, then drops to
# UID 1000 via su-exec and execs the rootless Docker daemon.
#
# Only the JAILOC-OUTPUT chain on the OUTPUT chain is used. The DOCKER-USER
# chain (Docker's FORWARD extension point) does not apply to rootless mode
# because rootlesskit routes inner container traffic through vpnkit, which
# exits via the outer network namespace's OUTPUT chain. This means all
# traffic — from the DinD container itself and from any inner containers —
# passes through JAILOC-OUTPUT.
#
# After iptables setup, su-exec switches to UID 1000. The kernel clears
# inheritable, permitted, effective, and ambient capabilities on the UID
# transition. The bounding set stays full but is unexploitable — no binary
# in the image can grant CAP_NET_ADMIN to UID 1000. --no-new-privs is not
# set because rootlesskit needs setuid newuidmap/newgidmap.

# --- Detect working iptables variant ---
if iptables -L -n >/dev/null 2>&1; then
  IPT=iptables
else
  IPTABLES_ERROR="$(iptables -L -n 2>&1 || true)"
  if command -v iptables-legacy >/dev/null 2>&1 && iptables-legacy -L -n >/dev/null 2>&1; then
    IPT=iptables-legacy
    echo "jailoc-dind: iptables unusable ($IPTABLES_ERROR), using iptables-legacy" >&2
  else
    echo "jailoc-dind: FATAL: no working iptables found, cannot enforce network isolation" >&2
    exit 1
  fi
fi

# --- JAILOC-OUTPUT chain: restrict all egress (DinD + inner containers) ---

$IPT -N JAILOC-OUTPUT 2>/dev/null || true
$IPT -F JAILOC-OUTPUT
$IPT -C OUTPUT -j JAILOC-OUTPUT 2>/dev/null || $IPT -I OUTPUT -j JAILOC-OUTPUT

$IPT -A JAILOC-OUTPUT -o lo -j ACCEPT
$IPT -A JAILOC-OUTPUT -o docker0 -j ACCEPT
$IPT -A JAILOC-OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# Allow DNS to configured resolvers (public DNS is permitted by the default
# ACCEPT policy; private-network DROPs below block internal resolvers).
DNS_RESOLVERS=$(
  awk '$1 == "nameserver" && $2 ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print $2 }' \
    /etc/resolv.conf | sort -u
)
for DNS_IP in $DNS_RESOLVERS; do
  $IPT -A JAILOC-OUTPUT -p udp -d "$DNS_IP" --dport 53 -j ACCEPT
  $IPT -A JAILOC-OUTPUT -p tcp -d "$DNS_IP" --dport 53 -j ACCEPT
done

ALLOWED_HOSTS="/etc/jailoc/allowed-hosts"
if [ -f "$ALLOWED_HOSTS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="$(echo "$line" | tr -d ' ')"
    [ -z "$line" ] && continue

    RESOLVED=$(getent hosts "$line" 2>/dev/null | awk '{print $1}' | grep -E '^[0-9]+\.' || true)
    if [ -n "$RESOLVED" ]; then
      for IP in $RESOLVED; do
        $IPT -A JAILOC-OUTPUT -d "$IP" -j ACCEPT
      done
    fi
  done < "$ALLOWED_HOSTS"
fi

ALLOWED_NETWORKS="/etc/jailoc/allowed-networks"
if [ -f "$ALLOWED_NETWORKS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="$(echo "$line" | tr -d ' ')"
    [ -z "$line" ] && continue

    $IPT -A JAILOC-OUTPUT -d "$line" -j ACCEPT
  done < "$ALLOWED_NETWORKS"
fi

# Block private/internal networks.
$IPT -A JAILOC-OUTPUT -d 10.0.0.0/8 -j DROP
$IPT -A JAILOC-OUTPUT -d 172.16.0.0/12 -j DROP
$IPT -A JAILOC-OUTPUT -d 192.168.0.0/16 -j DROP
$IPT -A JAILOC-OUTPUT -d 169.254.0.0/16 -j DROP
$IPT -A JAILOC-OUTPUT -d 100.64.0.0/10 -j DROP

if [ "${JAILOC_FILTERED_DNS:-}" = 1 ]; then
  $IPT -I JAILOC-OUTPUT -p tcp --dport 53 -j REJECT
  $IPT -I JAILOC-OUTPUT -p udp --dport 53 -j REJECT
  $IPT -I JAILOC-OUTPUT -p tcp -d 169.254.53.53 --dport 53 -j ACCEPT
  $IPT -I JAILOC-OUTPUT -p udp -d 169.254.53.53 --dport 53 -j ACCEPT
  $IPT -I JAILOC-OUTPUT -p tcp -d 127.0.0.11 --dport 53 -j ACCEPT
  $IPT -I JAILOC-OUTPUT -p udp -d 127.0.0.11 --dport 53 -j ACCEPT
fi

# --- Prepare rootless data directories ---
# The rootless Docker daemon stores data under the rootless user's home.
# Named volumes are created as root by Docker; fix ownership before dropping.
ROOTLESS_HOME="/home/rootless"
mkdir -p "$ROOTLESS_HOME/.local/share/docker" "$ROOTLESS_HOME/.config/docker"
if [ "$(stat -c '%u' "$ROOTLESS_HOME/.local/share/docker" 2>/dev/null)" != "1000" ] || \
   [ "$(stat -c '%u' "$ROOTLESS_HOME/.config/docker" 2>/dev/null)" != "1000" ]; then
  chown -R 1000:1000 "$ROOTLESS_HOME/.local/share/docker" "$ROOTLESS_HOME/.config/docker"
fi

# Native overlayfs requires kernel support for mounts inside user namespaces.
# Fall back to fuse-overlayfs on older kernels and disable the containerd
# snapshotter, which only supports native overlayfs.
apk add --no-cache su-exec >/dev/null 2>&1 || true

run_rootless() {
  if command -v su-exec >/dev/null 2>&1; then
    su-exec rootless sh -c "$1"
  else
    su rootless -s /bin/sh -c "$1"
  fi
}

DAEMON_CONFIG="$ROOTLESS_HOME/.config/docker/daemon.json"
FALLBACK_MARKER="/var/lib/jailoc/dind-overlay-fallback"
mkdir -p "$(dirname "$FALLBACK_MARKER")"

if [ -L "$DAEMON_CONFIG" ]; then
  echo "jailoc-dind: FATAL: refusing symlinked Docker daemon config at $DAEMON_CONFIG" >&2
  exit 1
fi

fallback_config() {
  cat <<'EOF'
{
  "storage-driver": "fuse-overlayfs",
  "features": {
    "containerd-snapshotter": false
  }
}
EOF
}

write_fallback_config() {
  TEMP_CONFIG=$(mktemp "$ROOTLESS_HOME/.config/docker/.daemon.json.XXXXXX")
  if ! fallback_config > "$TEMP_CONFIG" ||
     ! chown 1000:1000 "$TEMP_CONFIG" ||
     ! mv -f "$TEMP_CONFIG" "$DAEMON_CONFIG"; then
    rm -f "$TEMP_CONFIG"
    return 1
  fi
}

if run_rootless '
  OVERLAY_TEST_DIR=$(mktemp -d "$HOME/.local/share/docker/.overlay-test.XXXXXX")
  mkdir -p "$OVERLAY_TEST_DIR/lower" "$OVERLAY_TEST_DIR/upper" \
           "$OVERLAY_TEST_DIR/work" "$OVERLAY_TEST_DIR/merged"
  OVERLAY_SUPPORTED=0
  for OVERLAY_OPTIONS in "userxattr," ""; do
    if unshare -U -m -r sh -c \
      "mount -t overlay overlay -o ${OVERLAY_OPTIONS}lowerdir=\"$OVERLAY_TEST_DIR/lower\",upperdir=\"$OVERLAY_TEST_DIR/upper\",workdir=\"$OVERLAY_TEST_DIR/work\" \"$OVERLAY_TEST_DIR/merged\" && umount \"$OVERLAY_TEST_DIR/merged\""; then
      OVERLAY_SUPPORTED=1
      break
    fi
    rmdir "$OVERLAY_TEST_DIR/work/work" 2>/dev/null || true
  done
  rmdir "$OVERLAY_TEST_DIR/work/work" 2>/dev/null || true
  if ! rmdir "$OVERLAY_TEST_DIR/merged" "$OVERLAY_TEST_DIR/work" \
             "$OVERLAY_TEST_DIR/upper" "$OVERLAY_TEST_DIR/lower" "$OVERLAY_TEST_DIR"; then
    exit 2
  fi
  [ "$OVERLAY_SUPPORTED" = 1 ]
' >/dev/null 2>&1; then
  PROBE_STATUS=0
else
  PROBE_STATUS=$?
fi

if [ "$PROBE_STATUS" -eq 2 ]; then
  echo "jailoc-dind: FATAL: could not clean up the native overlayfs probe" >&2
  exit 1
fi

if [ "$PROBE_STATUS" -ne 0 ]; then
  if [ -e "$DAEMON_CONFIG" ] && [ ! -f "$FALLBACK_MARKER" ]; then
    echo "jailoc-dind: FATAL: native overlayfs is unavailable and $DAEMON_CONFIG is not managed by jailoc" >&2
    exit 1
  fi
  if [ -e "$DAEMON_CONFIG" ] && ! fallback_config | cmp -s - "$DAEMON_CONFIG"; then
    echo "jailoc-dind: FATAL: native overlayfs is unavailable and $DAEMON_CONFIG was modified after jailoc created it" >&2
    exit 1
  fi
  if [ ! -e "$DAEMON_CONFIG" ]; then
    if ! write_fallback_config; then
      echo "jailoc-dind: FATAL: could not write fallback Docker daemon config at $DAEMON_CONFIG" >&2
      exit 1
    fi
  fi
  touch "$FALLBACK_MARKER"
elif [ -f "$FALLBACK_MARKER" ]; then
  if fallback_config | cmp -s - "$DAEMON_CONFIG"; then
    rm -f "$DAEMON_CONFIG" "$FALLBACK_MARKER"
  else
    rm -f "$FALLBACK_MARKER"
  fi
fi

# TLS cert volumes are created as root; the upstream dockerd-entrypoint.sh
# generates certs and needs write access as UID 1000.
for d in /certs/ca /certs/client; do
  if [ -d "$d" ] && [ "$(stat -c '%u' "$d" 2>/dev/null)" != "1000" ]; then
    chown -R 1000:1000 "$d"
  fi
done

# --- Install forwarded CA bundle ---
CA_BUNDLE="/etc/jailoc/ca-bundle.pem"
SYSTEM_CA="/etc/ssl/certs/ca-certificates.crt"
ORIGINAL_CA="/etc/ssl/certs/ca-certificates.jailoc-original.crt"
if [ -e "$CA_BUNDLE" ]; then
  if [ ! -f "$CA_BUNDLE" ] || [ ! -s "$CA_BUNDLE" ]; then
    echo "jailoc-dind: FATAL: forwarded CA bundle must be a non-empty regular file: $CA_BUNDLE" >&2
    exit 1
  fi
  if [ ! -f "$ORIGINAL_CA" ]; then
    cp "$SYSTEM_CA" "$ORIGINAL_CA"
  fi
  if ! cp "$ORIGINAL_CA" "$SYSTEM_CA" || ! cat "$CA_BUNDLE" >> "$SYSTEM_CA"; then
    echo "jailoc-dind: FATAL: could not install forwarded CA bundle in $SYSTEM_CA" >&2
    exit 1
  fi
fi
if [ -f "$ORIGINAL_CA" ] && [ ! -e "$CA_BUNDLE" ]; then
  cp "$ORIGINAL_CA" "$SYSTEM_CA"
fi

# --- Clean stale containerd state ---
# Prevents "containerd is still running" crash loop when PID file persists
# on volume from prior unclean shutdown.
# See: https://github.com/moby/moby/blob/v28.1.1/cmd/dockerd/daemon.go#L146-L160
rm -f "$ROOTLESS_HOME/.local/share/docker/containerd/containerd.pid" \
      "$ROOTLESS_HOME/.local/share/docker/containerd/containerd.sock" \
      "$ROOTLESS_HOME/.local/share/docker/containerd/containerd-debug.sock"

# --- Drop privileges and exec rootless dockerd ---
# su-exec switches to UID 1000 (rootless) in a single exec. The kernel
# automatically clears inheritable, permitted, effective, and ambient
# capability sets on the UID transition. The bounding set remains full but
# is unexploitable: the only setuid binary in the image (fusermount3) has
# no file capabilities, so UID 1000 cannot regain CAP_NET_ADMIN to modify
# iptables rules. --no-new-privs is not set because rootlesskit needs
# setuid newuidmap/newgidmap for user namespace setup.
exec su-exec rootless env HOME="$ROOTLESS_HOME" dockerd-entrypoint.sh "$@"
