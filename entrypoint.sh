#!/bin/bash
set -euo pipefail

iptables -I OUTPUT -d dind -j ACCEPT

iptables -A OUTPUT -d 10.0.0.0/8 -j DROP
iptables -A OUTPUT -d 172.16.0.0/12 -j DROP
iptables -A OUTPUT -d 192.168.0.0/16 -j DROP
iptables -A OUTPUT -d 169.254.0.0/16 -j DROP
iptables -A OUTPUT -d 100.64.0.0/10 -j DROP

chown -R 1000:1000 /home/agent/.local

exec setpriv --reuid=1000 --regid=1000 --init-groups --inh-caps=-all --no-new-privs -- env HOME=/home/agent "$@"
