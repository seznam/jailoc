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

## Versioning

Tool versions are declared as `ARG` blocks at the top of the `Dockerfile`. Each `ARG` line is preceded by a `# renovate:` comment that tells Renovate bot which datasource and package to track. Renovate reads these annotations and opens PRs to bump versions automatically.

To pin a specific version, edit the corresponding `ARG` value in the `Dockerfile` directly. The `renovate.json` file contains the custom regex manager config that teaches Renovate how to parse these annotations.

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
| `~/jailoc` | `/workspace` | No | Project files |
| `~/.config/opencode` | `/home/agent/.config/opencode` | Yes | OpenCode config (providers, plugins, MCPs, rules) |
| `~/.opencode` | `/home/agent/.opencode` | Yes | OpenCode project agents/skills/plugins |
| `~/.claude` | `/home/agent/.claude` | No | Claude hooks, CLAUDE.md, skill symlinks, transcripts |
| `~/.agents` | `/home/agent/.agents` | Yes | Agent skill targets (symlink resolution) |
| `~/.config/jailoc` | `/etc/jailoc` | Yes | Firewall allowed-hosts config |
| `opencode-data` (named volume) | `/home/agent/.local/share/opencode` | No | Isolated DB, auth tokens, plugin state |
| `opencode-cache` (named volume) | `/home/agent/.cache` | No | Plugin cache (bun installs) |
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

### Allowing Internal Hosts

To allow specific internal hostnames through the firewall (e.g., internal MCP servers), create `~/.config/jailoc/allowed-hosts`:

```
document-search.asistent.ftxt.dszn.cz
mcp.ai.iszn.cz
```

One hostname per line. Lines starting with `#` are comments. Hostnames are resolved at container startup and added as iptables ACCEPT rules before the DROP rules. See `allowed-hosts.example` for reference.
