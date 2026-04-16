#!/bin/bash
set -euo pipefail

# --- Detect working iptables variant ---
# Ubuntu 24.04 defaults to iptables-nft, which requires kernel nf_tables support.
# Some environments (e.g. Rancher Desktop with VZ virtualization) lack this kernel
# module, so we fall back to iptables-legacy. Abort if neither works — network
# isolation is non-negotiable.
if iptables -L -n >/dev/null 2>&1; then
  IPT=iptables
else
  IPTABLES_ERROR="$(iptables -L -n 2>&1 || true)"
  if command -v iptables-legacy >/dev/null 2>&1 && iptables-legacy -L -n >/dev/null 2>&1; then
    IPT=iptables-legacy
    if [ -n "$IPTABLES_ERROR" ]; then
      echo "jailoc: iptables unusable ($IPTABLES_ERROR), using iptables-legacy" >&2
    else
      echo "jailoc: iptables unusable, using iptables-legacy" >&2
    fi
  else
    echo "jailoc: FATAL: no working iptables found, cannot enforce network isolation" >&2
    exit 1
  fi
fi

# --- Allow infrastructure targets ---
$IPT -I OUTPUT -d dind -j ACCEPT

HOST_IP=$(getent hosts host.docker.internal | awk '{print $1}' || true)
if [ -n "$HOST_IP" ]; then
  $IPT -I OUTPUT -d "$HOST_IP" -j ACCEPT
fi

for GW in $(awk '$3 != "00000000" && $3 != "Gateway" {print $3}' /proc/net/route | sort -u); do
  GW_IP=$(printf '%d.%d.%d.%d' "0x${GW:6:2}" "0x${GW:4:2}" "0x${GW:2:2}" "0x${GW:0:2}")
  $IPT -I OUTPUT -d "$GW_IP" -j ACCEPT
done

# --- Allow hostnames from config ---
ALLOWED_HOSTS="/etc/jailoc/allowed-hosts"
if [ -f "$ALLOWED_HOSTS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="${line// /}"
    [ -z "$line" ] && continue

    RESOLVED=$(getent hosts "$line" | awk '{print $1}' || true)
    if [ -n "$RESOLVED" ]; then
      for IP in $RESOLVED; do
        $IPT -I OUTPUT -d "$IP" -j ACCEPT
        echo "jailoc: allow $line ($IP)"
      done
    else
      echo "jailoc: WARNING: could not resolve $line" >&2
    fi
  done < "$ALLOWED_HOSTS"
fi

# --- Allow DNS resolvers (port 53 only) ---
if [ -f /etc/resolv.conf ]; then
  while read -r key value _; do
    if [ "$key" = "nameserver" ]; then
      if [[ "$value" == *:* ]]; then
        continue
      fi
      $IPT -I OUTPUT -p udp -d "$value" --dport 53 -j ACCEPT
      $IPT -I OUTPUT -p tcp -d "$value" --dport 53 -j ACCEPT
    fi
  done < /etc/resolv.conf
fi

# --- Allow CIDR networks from config ---
ALLOWED_NETWORKS="/etc/jailoc/allowed-networks"
if [ -f "$ALLOWED_NETWORKS" ]; then
  while IFS= read -r line; do
    line="${line%%#*}"
    line="${line// /}"
    [ -z "$line" ] && continue

    $IPT -I OUTPUT -d "$line" -j ACCEPT
    echo "jailoc: allow network $line"
  done < "$ALLOWED_NETWORKS"
fi

# --- Allow replies to inbound connections on the published service port ---
$IPT -A OUTPUT -p tcp --sport 4096 -m conntrack --ctstate ESTABLISHED -j ACCEPT

# --- Block private/internal networks ---
$IPT -A OUTPUT -d 10.0.0.0/8 -j DROP
$IPT -A OUTPUT -d 172.16.0.0/12 -j DROP
$IPT -A OUTPUT -d 192.168.0.0/16 -j DROP
$IPT -A OUTPUT -d 169.254.0.0/16 -j DROP
$IPT -A OUTPUT -d 100.64.0.0/10 -j DROP

chown -R 1000:1000 /home/agent/.local /home/agent/.cache 2>/dev/null || true
chown 1000:1000 /home/agent/.claude 2>/dev/null || true

if [ -S /run/ssh-agent.sock ]; then
  chown 1000:1000 /run/ssh-agent.sock 2>/dev/null || true
fi

if [ -d /home/agent/.ssh ]; then
  chown 1000:1000 /home/agent/.ssh 2>/dev/null || true
fi

exec setpriv --reuid=1000 --regid=1000 --init-groups --inh-caps=-all --no-new-privs -- env HOME=/home/agent "$@"
