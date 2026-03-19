# 🔒 Network Isolation

On container startup, `iptables` rules block outbound traffic to private and internal address ranges:

- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (RFC 1918)
- `169.254.0.0/16` (link-local)
- `100.64.0.0/10` (CGNAT)

Public internet remains open 🌍, which the agent needs for `git`, `npm`, `pip`, `go get`, and MCP server calls.

To allow specific internal endpoints, use `allowed_hosts` (resolved by hostname at startup) or `allowed_networks` (CIDR ranges) in your workspace config. ACCEPT rules are inserted before the DROP rules, so allowlisted targets are reachable even if they fall inside a blocked range.

The DinD sidecar communicates over an internal Docker network that is not subject to these iptables rules.

## 🛡️ Security

### ✅ What IS isolated

- 🔐 Non-root user (`agent`, UID 1000) with passwordless sudo
- 🚫 All Linux capabilities dropped except the minimum set needed for iptables and privilege drop
- 📏 Resource limits: 4 GB RAM, 2 CPUs, 256 PIDs
- 🔒 OpenCode config dirs mounted read-only
- 💾 Isolated data volume: the agent's SQLite DB and auth tokens don't touch `~/.local/share/opencode` on the host
- 🐳 Docker-in-Docker: no host socket mount; containers the agent spawns run inside the DinD daemon only
- 🌐 Network egress to private/internal ranges blocked by iptables

### ⚠️ What is NOT isolated

- ⚡ DinD sidecar runs `--privileged` (required for nested Docker)
- 🌍 Public internet is fully open
- 🔑 API keys in your mounted `opencode.json` are readable inside the container
- 📭 No seccomp or AppArmor profile beyond Docker defaults
- 📝 No read-only root filesystem
