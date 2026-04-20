#!/bin/sh
set -eu

# Network isolation for the DinD sidecar.
#
# Without these rules, an agent in the opencode container can bypass all
# iptables restrictions by creating a container inside DinD with unrestricted
# network access.
#
# Rules go into the DOCKER-USER chain — the official Docker extension point
# for user firewall rules. Docker evaluates DOCKER-USER before its own
# FORWARD rules, so our DROPs fire first regardless of Docker version.
# We pre-create the chain; dockerd will add the FORWARD → DOCKER-USER jump.
#
# FORWARD rules use the default-route interface so only traffic leaving DinD
# toward the compose network (and beyond) is filtered. Inter-container traffic
# on Docker bridge interfaces (docker0, br-*) is unaffected.

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

# --- DOCKER-USER chain: restrict inner containers ---

DEFAULT_IF=$(ip route show default | awk '{print $5}' | head -1)
if [ -z "$DEFAULT_IF" ]; then
  echo "jailoc-dind: FATAL: no default route interface found" >&2
  exit 1
fi

$IPT -N DOCKER-USER 2>/dev/null || true
$IPT -F DOCKER-USER

$IPT -A DOCKER-USER -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN

# Allow DNS only to configured resolvers (from compose dns: entries in
# /etc/resolv.conf), not to arbitrary destinations on port 53.
DNS_RESOLVERS=$(
  awk '$1 == "nameserver" && $2 ~ /^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ { print $2 }' \
    /etc/resolv.conf | sort -u
)
for DNS_IP in $DNS_RESOLVERS; do
  $IPT -A DOCKER-USER -o "$DEFAULT_IF" -p udp -d "$DNS_IP" --dport 53 -j RETURN
  $IPT -A DOCKER-USER -o "$DEFAULT_IF" -p tcp -d "$DNS_IP" --dport 53 -j RETURN
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
        $IPT -A DOCKER-USER -o "$DEFAULT_IF" -d "$IP" -j RETURN
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

    $IPT -A DOCKER-USER -o "$DEFAULT_IF" -d "$line" -j RETURN
  done < "$ALLOWED_NETWORKS"
fi

# Block inner containers from reaching private/internal networks.
$IPT -A DOCKER-USER -o "$DEFAULT_IF" -d 10.0.0.0/8 -j DROP
$IPT -A DOCKER-USER -o "$DEFAULT_IF" -d 172.16.0.0/12 -j DROP
$IPT -A DOCKER-USER -o "$DEFAULT_IF" -d 192.168.0.0/16 -j DROP
$IPT -A DOCKER-USER -o "$DEFAULT_IF" -d 169.254.0.0/16 -j DROP
$IPT -A DOCKER-USER -o "$DEFAULT_IF" -d 100.64.0.0/10 -j DROP

$IPT -A DOCKER-USER -j RETURN

# --- OUTPUT chain: restrict DinD's own traffic ---

$IPT -N JAILOC-OUTPUT 2>/dev/null || true
$IPT -F JAILOC-OUTPUT
$IPT -C OUTPUT -j JAILOC-OUTPUT 2>/dev/null || $IPT -I OUTPUT -j JAILOC-OUTPUT

$IPT -A JAILOC-OUTPUT -o lo -j ACCEPT
$IPT -A JAILOC-OUTPUT -o docker0 -j ACCEPT
$IPT -A JAILOC-OUTPUT -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT

for DNS_IP in $DNS_RESOLVERS; do
  $IPT -A JAILOC-OUTPUT -p udp -d "$DNS_IP" --dport 53 -j ACCEPT
  $IPT -A JAILOC-OUTPUT -p tcp -d "$DNS_IP" --dport 53 -j ACCEPT
done

$IPT -A JAILOC-OUTPUT -d 10.0.0.0/8 -j DROP
$IPT -A JAILOC-OUTPUT -d 172.16.0.0/12 -j DROP
$IPT -A JAILOC-OUTPUT -d 192.168.0.0/16 -j DROP
$IPT -A JAILOC-OUTPUT -d 169.254.0.0/16 -j DROP
$IPT -A JAILOC-OUTPUT -d 100.64.0.0/10 -j DROP

# --- Clean stale containerd state ---
# Prevents "containerd is still running" crash loop when PID file persists
# on volume from prior unclean shutdown.
# See: https://github.com/moby/moby/blob/v28.1.1/cmd/dockerd/daemon.go#L146-L160
rm -f /var/lib/docker/containerd/containerd.pid \
      /var/lib/docker/containerd/containerd.sock \
      /var/lib/docker/containerd/containerd-debug.sock

exec dockerd-entrypoint.sh "$@"
