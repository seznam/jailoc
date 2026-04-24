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

# --- Prepare rootless data directories ---
# The rootless Docker daemon stores data under the rootless user's home.
# Named volumes are created as root by Docker; fix ownership before dropping.
ROOTLESS_HOME="/home/rootless"
mkdir -p "$ROOTLESS_HOME/.local/share/docker" "$ROOTLESS_HOME/.config/docker"
if [ "$(stat -c '%u' "$ROOTLESS_HOME/.local/share/docker" 2>/dev/null)" != "1000" ] || \
   [ "$(stat -c '%u' "$ROOTLESS_HOME/.config/docker" 2>/dev/null)" != "1000" ]; then
  chown -R 1000:1000 "$ROOTLESS_HOME/.local/share/docker" "$ROOTLESS_HOME/.config/docker"
fi
# TLS cert volumes are created as root; the upstream dockerd-entrypoint.sh
# generates certs and needs write access as UID 1000.
for d in /certs/ca /certs/client; do
  if [ -d "$d" ] && [ "$(stat -c '%u' "$d" 2>/dev/null)" != "1000" ]; then
    chown -R 1000:1000 "$d"
  fi
done

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
apk add --no-cache su-exec >/dev/null 2>&1 || true
exec su-exec rootless env HOME="$ROOTLESS_HOME" dockerd-entrypoint.sh "$@"
