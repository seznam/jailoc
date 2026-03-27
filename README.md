![jailoc](docs/hero.jpeg)

# jailoc

[![CI](https://github.com/seznam/jailoc/actions/workflows/ci.yml/badge.svg)](https://github.com/seznam/jailoc/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seznam/jailoc)](https://github.com/seznam/jailoc/releases/latest)

Manage sandboxed Docker Compose environments for headless OpenCode coding agents.

📖 **[Full documentation](https://seznam.github.io/jailoc/)**

## What is this?

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network isolation that blocks private networks by default, letting you control exactly which internal services the agent can reach. You configure which directories to mount as workspaces, which hosts to allowlist, and the agent runs inside with your OpenCode config available read-only.

## Why jailoc

- 📁 **File isolation** — the agent only sees directories you explicitly mount. SSH keys, browser profiles, and other projects are invisible. It runs as UID 1000 with all Linux capabilities dropped and `no_new_privs` set.
- 🌐 **Network isolation** — private networks (RFC 1918, link-local, CGNAT) are blocked by default via iptables. You allowlist only what the agent needs. No pivoting to internal infrastructure.
- 🐳 **Sandboxed Docker** — each workspace gets its own Docker daemon via a DinD sidecar. No host socket mounting, no sandbox escape through container breakout.
- ⚡ **Zero config to start** — `jailoc up` handles image resolution, compose generation, firewall setup, and privilege dropping automatically.

## Installation

**Prerequisites:** Docker Engine must be running. No `docker compose` CLI plugin needed — jailoc embeds the Compose SDK.

### go install

```bash
go install github.com/seznam/jailoc/cmd/jailoc@latest
```

Make sure `$GOPATH/bin` (default `$HOME/go/bin`) is on your `PATH`.

### Pre-built binaries

Download the archive for your platform from [GitHub Releases](https://github.com/seznam/jailoc/releases) (Linux/macOS × amd64/arm64), extract, and place the `jailoc` binary on your `PATH`.

## 🛠️ Development

```bash
# Build from source
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run integration tests (requires Docker)
go test -tags=integration ./...
```

## 📦 What's in the default container

The default base image (Ubuntu 24.04) ships with:

| Category | Tools |
|----------|-------|
| Runtimes | Node.js, Python 3 |
| Package managers | npm |
| Language servers | typescript-language-server, pyright, yaml-language-server, bash-language-server |
| CLI tools | rg (ripgrep), fdfind, jq, git, openssh-client, curl, sudo |
| Agent stack | OpenCode |

Exact versions are pinned in the [embedded Dockerfile](internal/embed/assets/Dockerfile) and tracked by Renovate. See the [default image reference](https://seznam.github.io/jailoc/reference/default-image/) for the full list of installed tools.
