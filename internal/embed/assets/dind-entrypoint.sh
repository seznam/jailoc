#!/bin/sh
set -eu

# Network isolation for the DinD sidecar.
#
# Without these rules, an agent in the opencode container can bypass all
# iptables restrictions by creating a container inside DinD with unrestricted
# network access. These FORWARD-chain rules ensure containers spawned inside
# DinD cannot reach private/internal networks.
#
# FORWARD rules use "-o eth0" so only traffic leaving DinD toward the
# compose network (and beyond) is filtered. Inter-container traffic on
# Docker bridge interfaces (docker0, br-*) is unaffected.

# --- FORWARD chain: restrict inner containers ---

# Allow return traffic for established connections.
iptables -A FORWARD -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# Allow DNS resolution for inner containers.
iptables -A FORWARD -p udp --dport 53 -j ACCEPT
iptables -A FORWARD -p tcp --dport 53 -j ACCEPT

# Allow configured hosts (resolved at opencode container startup and shared
# via the same allowed-hosts file).
ALLOWED_HOSTS="/etc/jailoc/allowed-hosts"
if [ -f "$ALLOWED_HOSTS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="$(echo "$line" | tr -d ' ')"
    [ -z "$line" ] && continue

    RESOLVED=$(getent hosts "$line" 2>/dev/null | awk '{print $1}' || true)
    if [ -n "$RESOLVED" ]; then
      for IP in $RESOLVED; do
        iptables -A FORWARD -o eth0 -d "$IP" -j ACCEPT
      done
    fi
  done < "$ALLOWED_HOSTS"
fi

# Allow configured CIDR networks.
ALLOWED_NETWORKS="/etc/jailoc/allowed-networks"
if [ -f "$ALLOWED_NETWORKS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="$(echo "$line" | tr -d ' ')"
    [ -z "$line" ] && continue

    iptables -A FORWARD -o eth0 -d "$line" -j ACCEPT
  done < "$ALLOWED_NETWORKS"
fi

# Block inner containers from reaching private/internal networks via eth0.
# Traffic staying on Docker bridge interfaces (docker0, br-*) is not matched
# because those use a different output interface.
iptables -A FORWARD -o eth0 -d 10.0.0.0/8 -j DROP
iptables -A FORWARD -o eth0 -d 172.16.0.0/12 -j DROP
iptables -A FORWARD -o eth0 -d 192.168.0.0/16 -j DROP
iptables -A FORWARD -o eth0 -d 169.254.0.0/16 -j DROP
iptables -A FORWARD -o eth0 -d 100.64.0.0/10 -j DROP

# --- OUTPUT chain: restrict DinD's own traffic ---

# Allow loopback and Docker bridge traffic.
iptables -A OUTPUT -o lo -j ACCEPT
iptables -A OUTPUT -o docker0 -j ACCEPT

# Allow return traffic (e.g. responses to opencode container on port 2376).
iptables -A OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

# Allow DNS.
iptables -A OUTPUT -p udp --dport 53 -j ACCEPT
iptables -A OUTPUT -p tcp --dport 53 -j ACCEPT

# Block DinD itself from reaching private/internal networks.
iptables -A OUTPUT -d 10.0.0.0/8 -j DROP
iptables -A OUTPUT -d 172.16.0.0/12 -j DROP
iptables -A OUTPUT -d 192.168.0.0/16 -j DROP
iptables -A OUTPUT -d 169.254.0.0/16 -j DROP
iptables -A OUTPUT -d 100.64.0.0/10 -j DROP

# --- Clean stale containerd state ---
# Prevents "containerd is still running" crash loop when PID file persists
# on volume from prior unclean shutdown.
# See: https://github.com/moby/moby/blob/v28.1.1/cmd/dockerd/daemon.go#L146-L160
rm -f /var/lib/docker/containerd/containerd.pid \
      /var/lib/docker/containerd/containerd.sock \
      /var/lib/docker/containerd/containerd-debug.sock

exec dockerd-entrypoint.sh "$@"
