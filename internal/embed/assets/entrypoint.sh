#!/bin/bash
set -euo pipefail

# --- Allow infrastructure targets ---
iptables -I OUTPUT -d dind -j ACCEPT

HOST_IP=$(getent hosts host.docker.internal | awk '{print $1}' || true)
if [ -n "$HOST_IP" ]; then
  iptables -I OUTPUT -d "$HOST_IP" -j ACCEPT
fi

for GW in $(awk '$3 != "00000000" && $3 != "Gateway" {print $3}' /proc/net/route | sort -u); do
  GW_IP=$(printf '%d.%d.%d.%d' "0x${GW:6:2}" "0x${GW:4:2}" "0x${GW:2:2}" "0x${GW:0:2}")
  iptables -I OUTPUT -d "$GW_IP" -j ACCEPT
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
        iptables -I OUTPUT -d "$IP" -j ACCEPT
        echo "jailoc: allow $line ($IP)"
      done
    else
      echo "jailoc: WARNING: could not resolve $line" >&2
    fi
  done < "$ALLOWED_HOSTS"
fi

# --- Block private/internal networks ---
iptables -A OUTPUT -d 10.0.0.0/8 -j DROP
iptables -A OUTPUT -d 172.16.0.0/12 -j DROP
iptables -A OUTPUT -d 192.168.0.0/16 -j DROP
iptables -A OUTPUT -d 169.254.0.0/16 -j DROP
iptables -A OUTPUT -d 100.64.0.0/10 -j DROP

chown -R 1000:1000 /home/agent/.local /home/agent/.cache

exec setpriv --reuid=1000 --regid=1000 --init-groups --inh-caps=-all --no-new-privs -- env HOME=/home/agent "$@"
