# jailoc

Sandboxed Docker environment for headless OpenCode+Omo coding agents.

## Quick Start

```bash
# Start the sandbox
OPENCODE_SERVER_PASSWORD=your-password docker compose up -d

# Attach from host TUI
opencode attach http://localhost:4096
```

The password is optional but recommended. Without it, the server accepts any connection on port 4096.

## What's Inside

| Tool | Version |
|------|---------|
| Ubuntu | 24.04 |
| Python | 3.12 (system) |
| Go | 1.24.1 |
| Node.js | 22 LTS |
| Bun | latest |
| Yarn | via corepack |
| Docker CLI + Compose plugin | latest stable (via DinD sidecar) |
| oh-my-openagent | 3.11.2 |
| ripgrep, fd-find, jq, fzf, git | apt latest |
| yaml-language-server | npm latest |
| jsonnet-language-server | 0.17.0 |
| helm_ls | 0.2.1 |
| opencode | 1.2.27 |

## Security

### What IS isolated

- Non-root user (`agent`, UID 1000) with passwordless sudo
- All Linux capabilities dropped (`cap_drop: ALL`) except `NET_BIND_SERVICE`, `NET_ADMIN`, `SETUID`, `SETGID`, `CHOWN`, `FOWNER`
- Entrypoint runs as root for iptables + volume chown, then drops to UID 1000 via `setpriv --inh-caps=-all --no-new-privs`
- Resource limits: 4 GB RAM, 2 CPUs, 256 PIDs
- Config dirs (`.config/opencode`, `.opencode`, `.claude`, `.agents`) mounted read-only
- Isolated data volume: separate SQLite DB, no leakage into host `~/.local/share/opencode`
- Docker-in-Docker: isolated Docker daemon (no host socket mount), containers run inside the sandbox
- Network egress: private/internal networks blocked (10/8, 172.16/12, 192.168/16, 169.254/16, 100.64/10); only public internet allowed

### What is NOT isolated

- DinD sidecar runs `--privileged` (required for nested Docker; isolated to its own daemon)
- Network is unrestricted to public internet (required for git, npm, pip, `go get`, MCP server calls)
- API keys in the mounted `opencode.json` are readable inside the container
- No seccomp or AppArmor profile beyond Docker defaults
- No read-only root filesystem (the agent needs write access to `/workspace` and the data volume)

Production-grade isolation would add gVisor or Firecracker, a credential proxy, and stricter network egress filtering (allowlist-based).

## Volume Mounts

| Mount | Container Path | Read-Only | Purpose |
|-------|---------------|-----------|---------|
| `./workspace` | `/workspace` | No | Project files |
| `~/.config/opencode` | `/home/agent/.config/opencode` | Yes | OpenCode config (providers, plugins, MCPs, rules) |
| `~/.opencode` | `/home/agent/.opencode` | Yes | OpenCode project agents/skills/plugins |
| `~/.claude` | `/home/agent/.claude` | Yes | Claude hooks, CLAUDE.md, skill symlinks |
| `~/.agents` | `/home/agent/.agents` | Yes | Agent skill targets (symlink resolution) |
| `opencode-data` (named volume) | `/home/agent/.local/share/opencode` | No | Isolated DB, auth tokens, plugin state |
| `dind-certs-client` (named volume) | `/certs/client` | Yes | TLS certs for DinD communication |
| `dind-data` (named volume) | `/var/lib/docker` | No | DinD daemon storage |

## First Run Notes

The data volume is isolated from the host. Any auth tokens (e.g., set up via `opencode providers`) must be configured inside the container on first run.

Config is mounted read-only from host dirs. Provider API keys in `opencode.json` are used as-is without any transformation.

## Configuration

All OpenCode config is mounted from host directories (see Volume Mounts above). Changes to host config files reflect immediately in the container with no rebuild needed. The image bakes in tool versions only; no config is baked in.

## Docker-in-Docker

The container runs its own isolated Docker daemon via a `docker:dind` sidecar connected over TLS. No host socket is mounted — containers started by the agent run inside the DinD daemon only.

The DinD sidecar requires `privileged: true` for nested container support.

## Network Isolation

On startup, iptables rules block egress to private/internal networks:
- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (RFC 1918)
- `169.254.0.0/16` (link-local), `100.64.0.0/10` (CGNAT)

Public internet access remains open (required for git, npm, pip, `go get`, MCP calls). The DinD sidecar communicates via an internal Docker network not subject to these rules.
