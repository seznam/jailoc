# Getting Started

This tutorial walks you through installing jailoc and running your first sandboxed workspace. By the end, you'll have an OpenCode agent running in an isolated container, connected to your project directory.

## Prerequisites

You need **Docker Engine** (the daemon) running on your machine. jailoc embeds the Compose SDK directly, so you don't need the `docker compose` CLI plugin installed separately.

Verify Docker is running:

```bash
docker info
```

## Install jailoc

Install the latest release with `go install`:

```bash
go install github.com/seznam/jailoc/cmd/jailoc@{{ version }}
```

Alternatively, download a pre-built binary from [GitHub Releases](https://github.com/seznam/jailoc/releases). GoReleaser produces binaries for Linux and macOS on both amd64 and arm64.

After installation, confirm it's on your `PATH`:

```bash
jailoc --version
```

## Run your first workspace

The quickest way to get started is to run `jailoc` without any arguments from your project directory:

```bash
cd ~/myproject
jailoc
```

Here's what happens on first run:

1. **Config creation** — jailoc writes a default config to `~/.config/jailoc/config.toml` if it doesn't exist yet.
2. **Workspace prompt** — if the current directory isn't part of any configured workspace, jailoc asks whether to add it. Confirm, and the workspace is saved to your config.
3. **Container start** — jailoc renders a `docker-compose.yml` from its embedded template and starts the `opencode` and `dind` containers.
4. **Attach** — once the containers are healthy, jailoc attaches your local OpenCode TUI to the agent running inside.

> **Tip:** Your project directory is bind-mounted at its original path inside the container. If your project lives at `/home/you/myproject`, the agent sees it at the same path. Running `jailoc` from a subdirectory (e.g. `~/myproject/src`) opens that subdirectory in the agent instead of the workspace root.

## Use subcommands directly

The bare `jailoc` shortcut is convenient, but you can also drive each step explicitly:

```bash
# Start the environment in the background
jailoc up
```

This separation is useful when you want to start the workspace and come back to it later, or when you're scripting workspace lifecycle from CI.

Other subcommands worth knowing:

```bash
jailoc status   # show running workspaces and their ports
jailoc logs     # tail container logs
jailoc down     # stop and remove the workspace containers
```

## What's running inside

When the workspace is up, two containers are active on an isolated Docker network:

- **opencode container** — runs `opencode serve` as UID 1000 with your workspace paths mounted read-write and your OpenCode config mounted read-only.
- **dind container** — provides a privileged Docker daemon over TLS on port 2376, so the agent can build and run containers if needed.

The entrypoint script sets iptables rules that block private network ranges by default before dropping privileges. The agent can reach the internet, but not your internal network, unless you explicitly allow it.

## Next steps

Now that you have a workspace running, you can dig into configuration:

- [Workspace Configuration](../how-to/workspace-configuration.md) — add more workspaces, set mount paths, and tune per-workspace options
- [Network Access](../how-to/network-access.md) — allow specific internal hosts or CIDR ranges
- [Custom Images](../how-to/custom-images.md) — build or pull a custom base image with your own toolchain
- [Access Modes](../how-to/access-modes.md) — control what the agent is allowed to do
- [Configuration Reference](../reference/configuration.md) — full reference for every config field
