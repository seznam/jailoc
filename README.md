![jailoc](docs/hero.jpeg)

# jailoc

Manage sandboxed Docker Compose environments for headless OpenCode coding agents.

📖 **[Full documentation](https://seznam.github.io/jailoc/)**

## What is this?

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network isolation that blocks private networks by default, letting you control exactly which internal services the agent can reach. You configure which directories to mount as workspaces, which hosts to allowlist, and the agent runs inside with your OpenCode config available read-only.

## Installation

**Prerequisites:** Docker Engine must be running. No `docker compose` CLI plugin needed — jailoc embeds the Compose SDK.

### go install

```bash
go install github.com/seznam/jailoc/cmd/jailoc@latest
```

Make sure `$GOPATH/bin` (default `$HOME/go/bin`) is on your `PATH`.

### Pre-built binaries

Download the archive for your platform from [GitHub Releases](https://github.com/seznam/jailoc/releases) (Linux/macOS × amd64/arm64), extract, and place the `jailoc` binary on your `PATH`.

## Development

```bash
# Build from source
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run integration tests (requires Docker)
go test -tags=integration ./...
```

## What's in the default container

The default base image (Ubuntu 24.04) ships with:

| Category | Tools |
|----------|-------|
| Runtimes | Go, Node.js, Bun, Python 3 + uv |
| Package managers | npm, Yarn (via corepack), Homebrew |
| Language servers | gopls, typescript-language-server, pyright, yaml-language-server, bash-language-server, jsonnet-language-server, helm-ls |
| CLI tools | Docker CLI, ripgrep, fd, fzf, jq, vim, git, openssh-client |
| Agent stack | OpenCode, oh-my-openagent |

Exact versions are pinned in the [embedded Dockerfile](internal/embed/assets/Dockerfile) and tracked by Renovate.
