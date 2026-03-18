![jailoc](docs/hero.jpeg)

# jailoc

Manage sandboxed Docker Compose environments for headless OpenCode coding agents.

## What is this?

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network isolation that blocks private networks by default, letting you control exactly which internal services the agent can reach. You configure which directories to mount as workspaces, which hosts to allowlist, and the agent runs inside with your OpenCode config available read-only.

## Installation

Requires Docker with Compose V2 (`docker compose`, not `docker-compose`).

```bash
go install github.com/seznam/jailoc/cmd/jailoc@latest
```

Alternatively, download a pre-built binary from [GitLab Releases](https://github.com/seznam/jailoc/releases) (built by GoReleaser for Linux and macOS, amd64 and arm64).

## Quick Start

The simplest way to get going is to run `jailoc` with no arguments from your project directory:

```bash
cd ~/myproject
jailoc
```

On first run, this creates `~/.config/jailoc/config.toml`. If the current directory isn't in any workspace yet, jailoc asks whether to add it. Then it starts the Docker Compose environment and attaches via `opencode attach`.

For explicit control, use the subcommands directly:

```bash
# Start the environment in the background
jailoc up

# Attach your local opencode TUI to it
jailoc attach
```

## Configuration

Config lives at `~/.config/jailoc/config.toml`. It's created automatically on first run with a `default` workspace.

```toml
[image]
# Override the base image registry (default: ghcr.io/seznam/jailoc)
# repository = "ghcr.io/seznam/jailoc"

[workspaces.default]
paths = ["/home/you/projects/myproject"]
# allowed_hosts = ["internal-mcp.example.com"]
# allowed_networks = ["10.10.5.0/24"]
# build_context = "~/.config/jailoc"
```

You can define multiple workspaces. Each runs on a separate port:

```toml
[workspaces.api]
paths = ["/home/you/projects/api", "/home/you/projects/shared-libs"]
allowed_hosts = ["internal-registry.example.com"]

[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = ["172.20.0.0/16"]
```

**Port allocation:** workspace names are sorted alphabetically, then ports are assigned starting at 4096. So with workspaces `api` and `frontend`, `api` gets port 4096 and `frontend` gets 4097. The `default` workspace is typically alone and gets 4096.

**`paths`** — directories to mount into the container at their original absolute path (e.g. `/home/you/projects/api` on the host becomes `/home/you/projects/api` inside the container). Paths under system directories (`/usr`, `/etc`, `/var`, `/home/agent`, …) are rejected to prevent conflicts with the container runtime. Supports `~` expansion.

**`allowed_hosts`** — hostnames resolved at container startup and added as iptables ACCEPT rules before the private-network DROP rules.

**`allowed_networks`** — CIDR ranges to allow explicitly (e.g. `10.10.5.0/24`).

**`build_context`** — path used as the Docker build context when building workspace-specific images. Defaults to `~/.config/jailoc`.

## Commands

Use `--workspace` / `-w` to target a specific workspace (default: `default`).

| Command | Description |
|---------|-------------|
| `jailoc` | Auto-detect workspace from CWD, prompt to add if missing, start if not running, then attach. |
| `jailoc up` | Start the Docker Compose environment for the workspace. No-op if already running. |
| `jailoc down` | Stop and remove the containers for the workspace. |
| `jailoc attach` | Attach to a running workspace using `opencode attach` on the host. |
| `jailoc status` | Show running status and port for each configured workspace. |
| `jailoc logs` | Stream container logs from the workspace environment. |
| `jailoc config` | Print the current resolved config. |
| `jailoc add` | Add the current directory to a workspace's paths. |

## Custom Images

There are three levels of image customization:

**1. Workspace-specific layer** — create `~/.config/jailoc/{name}.Dockerfile`. This file is built on top of the resolved base image using `ARG BASE`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc passes the base image tag as `--build-arg BASE=...` and tags the result `jailoc-{name}:latest`.

**2. Full base override** — create `~/.config/jailoc/Dockerfile`. This replaces the entire base image. jailoc builds it as `jailoc-base:local` and uses it instead of pulling from the registry. Use this if you need to completely swap out the base.

**3. Default behavior (no custom files)** — jailoc pulls the versioned image from the configured registry. If the pull fails, it falls back to an embedded Dockerfile baked into the binary and builds `jailoc-base:embedded` locally.

The workspace layer (step 1) is always applied on top of whatever base was resolved.

## Network Isolation

On container startup, `iptables` rules block outbound traffic to private and internal address ranges:

- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (RFC 1918)
- `169.254.0.0/16` (link-local)
- `100.64.0.0/10` (CGNAT)

Public internet remains open, which the agent needs for `git`, `npm`, `pip`, `go get`, and MCP server calls.

To allow specific internal endpoints, use `allowed_hosts` (resolved by hostname at startup) or `allowed_networks` (CIDR ranges) in your workspace config. ACCEPT rules are inserted before the DROP rules, so allowlisted targets are reachable even if they fall inside a blocked range.

The DinD sidecar communicates over an internal Docker network that is not subject to these iptables rules.

## How it works

When you run any jailoc command, it reads `~/.config/jailoc/config.toml`, creating it with defaults if it doesn't exist yet.

**Workspace resolution** matches workspace paths against the current working directory. Port numbers are computed by sorting all workspace names alphabetically and assigning `4096 + index`.

**Image resolution** follows four steps in order:
1. If `~/.config/jailoc/Dockerfile` exists, build it as the base.
2. Otherwise, try pulling `{repository}:{version}` from the registry.
3. If the pull fails, build from the embedded fallback Dockerfile (baked into the binary at compile time).
4. If `~/.config/jailoc/{workspace}.Dockerfile` exists, build a workspace layer on top of whichever base was resolved.

**Compose file generation** — jailoc renders a `docker-compose.yml` from an embedded Go template and writes it to `~/.cache/jailoc/{workspace}/docker-compose.yml`. This cached file is what `docker compose` commands use.

**Docker Compose orchestration** — two services run: the `opencode` service (the agent container) and a `dind` sidecar that provides an isolated Docker daemon. The agent communicates with the DinD daemon over TLS using a shared named volume for certificates. No host Docker socket is mounted.

**Entrypoint** — the container starts as root so it can set up iptables rules and chown the data volume. It then drops to UID 1000 (`agent`) via `setpriv --inh-caps=-all --no-new-privs` before executing the OpenCode server process.

**Volume mounts** — workspace paths are bind-mounted at their original absolute path (host path = container path). OpenCode config directories (`~/.config/opencode`, `~/.opencode`, `~/.claude`, `~/.agents`) are mounted read-only. An isolated named volume holds the OpenCode data directory, keeping the agent's database and auth tokens separate from the host.

## Security

### What IS isolated

- Non-root user (`agent`, UID 1000) with passwordless sudo
- All Linux capabilities dropped except the minimum set needed for iptables and privilege drop
- Resource limits: 4 GB RAM, 2 CPUs, 256 PIDs
- OpenCode config dirs mounted read-only
- Isolated data volume: the agent's SQLite DB and auth tokens don't touch `~/.local/share/opencode` on the host
- Docker-in-Docker: no host socket mount; containers the agent spawns run inside the DinD daemon only
- Network egress to private/internal ranges blocked by iptables

### What is NOT isolated

- DinD sidecar runs `--privileged` (required for nested Docker)
- Public internet is fully open
- API keys in your mounted `opencode.json` are readable inside the container
- No seccomp or AppArmor profile beyond Docker defaults
- No read-only root filesystem

## Development

```bash
# Build the binary
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run integration tests (requires Docker)
go test -tags=integration ./...
```
