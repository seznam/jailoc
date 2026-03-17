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
| Docker CLI + Compose plugin | latest stable |
| ripgrep, fd-find, jq, fzf, git | apt latest |
| yaml-language-server | npm latest |
| jsonnet-language-server | 0.17.0 |
| helm_ls | 0.2.1 |
| opencode | 1.2.27 |

## Security

### What IS isolated

- Non-root user (`agent`, UID 1000) with passwordless sudo
- All Linux capabilities dropped (`cap_drop: ALL`) except `NET_BIND_SERVICE`
- `no-new-privileges` prevents privilege escalation via setuid binaries
- Resource limits: 4 GB RAM, 2 CPUs, 256 PIDs
- Config dirs (`.config/opencode`, `.opencode`, `.claude`, `.agents`) mounted read-only
- Isolated data volume: separate SQLite DB, no leakage into host `~/.local/share/opencode`

### What is NOT isolated

- Docker socket mount gives the container full control over the host Docker daemon (can launch, exec, or stop any container on the host)
- Network is unrestricted (required for git, npm, pip, `go get`, MCP server calls)
- API keys in the mounted `opencode.json` are readable inside the container
- No seccomp or AppArmor profile beyond Docker defaults
- No read-only root filesystem (the agent needs write access to `/workspace` and the data volume)

Production-grade isolation would add gVisor or Firecracker, a credential proxy, and network egress filtering.

## Volume Mounts

| Mount | Container Path | Read-Only | Purpose |
|-------|---------------|-----------|---------|
| `./workspace` | `/workspace` | No | Project files |
| `/var/run/docker.sock` | `/var/run/docker.sock` | No | Docker-in-Docker via host daemon |
| `~/.config/opencode` | `/home/agent/.config/opencode` | Yes | OpenCode config (providers, plugins, MCPs, rules) |
| `~/.opencode` | `/home/agent/.opencode` | Yes | OpenCode project agents/skills/plugins |
| `~/.claude` | `/home/agent/.claude` | Yes | Claude hooks, CLAUDE.md, skill symlinks |
| `~/.agents` | `/home/agent/.agents` | Yes | Agent skill targets (symlink resolution) |
| `opencode-data` (named volume) | `/home/agent/.local/share/opencode` | No | Isolated DB, auth tokens, plugin state |

## First Run Notes

The data volume is isolated from the host. Any auth tokens (e.g., set up via `opencode providers`) must be configured inside the container on first run.

Config is mounted read-only from host dirs. Provider API keys in `opencode.json` are used as-is without any transformation.

## Configuration

All OpenCode config is mounted from host directories (see Volume Mounts above). Changes to host config files reflect immediately in the container with no rebuild needed. The image bakes in tool versions only; no config is baked in.

## Docker-in-Docker

The container accesses the host Docker daemon via socket mount. This is simpler than true DinD: no nested daemon, no `--privileged` flag required.

The trade-off is that the container can start, stop, or exec into any container running on the host daemon.
