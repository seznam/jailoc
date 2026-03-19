# 🚀 Getting Started

## 📦 Installation

Requires Docker Engine (the daemon) 🐳. jailoc embeds the Compose SDK — no `docker compose` CLI plugin needed.

```bash
go install github.com/seznam/jailoc/cmd/jailoc@latest
```

Alternatively, download a pre-built binary from [GitLab Releases](https://github.com/seznam/jailoc/releases) 📥 (built by GoReleaser for Linux and macOS, amd64 and arm64).

## ⚡ Quick Start

The simplest way to get going is to run `jailoc` with no arguments from your project directory:

```bash
cd ~/myproject
jailoc
```

On first run, this creates `~/.config/jailoc/config.toml`. If the current directory isn't in any workspace yet, jailoc asks whether to add it. Then it starts the Docker Compose environment and attaches via `opencode attach`. ✨

For explicit control, use the subcommands directly:

```bash
# Start the environment in the background
jailoc up

# Attach your local opencode TUI to it
jailoc attach
```
