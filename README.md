![jailoc](docs/hero.jpeg)

# jailoc

[![CI](https://github.com/seznam/jailoc/actions/workflows/ci.yml/badge.svg)](https://github.com/seznam/jailoc/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seznam/jailoc)](https://github.com/seznam/jailoc/releases/latest)
[![Downloads](https://img.shields.io/github/downloads/seznam/jailoc/total)](https://github.com/seznam/jailoc/releases)
[![Homebrew Cask](https://img.shields.io/badge/dynamic/regex?url=https%3A%2F%2Fraw.githubusercontent.com%2Fseznam%2Fhomebrew-tap%2Fmaster%2FCasks%2Fjailoc.rb&search=version%20%22(.*)%22&replace=%241&label=homebrew%20cask&color=orange&logo=homebrew)](https://github.com/seznam/homebrew-tap)
[![Go Version](https://img.shields.io/github/go-mod/go-version/seznam/jailoc)](go.mod)
[![License](https://img.shields.io/github/license/seznam/jailoc)](LICENSE)

Give your coding agent its own computer, not your whole host. jailoc runs OpenCode, Claude Code, GitHub Copilot CLI, OmO or Antigravity CLI in a libkrun micro-VM per workspace, with only the directories you share and only the network your rules allow.

📖 **[Full documentation](https://seznam.github.io/jailoc/)**

## What is this?

Each workspace boots its own VM with its own kernel. Your project directories are shared into it, a network broker on the host decides every connection it makes, and the agent runs in detachable sessions you attach to over SSH. Run `jailoc` in a project directory and you're talking to the agent.

## Why jailoc

- 📁 **Only the files you share**: the agent sees the directories you share and read-only snapshots of files you list. Your SSH keys, browser profiles and other projects aren't in the VM.
- 🌐 **Egress by rule**: destinations no allow rule matches are denied, apart from the coding agent's own endpoints. Private and reserved IPv4 and IPv6 ranges stay closed unless a rule opens them, so the agent can't wander into your internal network.
- 🐳 **Docker stays inside**: with `enable_docker = true` the Docker daemon runs in the workspace VM. No host socket mounting.
- ⚡ **One command to start**: `jailoc` unpacks its embedded VM runtime, builds the image, boots the VM and attaches you to the agent.

> [!WARNING]
> Shared directories are read-write: the agent's edits land on your host at once, including Git hooks that run the next time you use Git there; [`protect_git`](https://seznam.github.io/jailoc/reference/configuration/#protect_git) covers only the repositories it applies to. Anything the agent can read can leave through the destinations your rules allow. [Security boundaries](https://seznam.github.io/jailoc/explanation/security-boundaries/#outside-the-boundaries) lists the exact limits, including the VMM confinement gaps on Linux and the Seatbelt deprecation risk on macOS.

## Installation

**Requirements:** macOS on Apple Silicon, or Linux amd64 (arm64 experimental) with read-write access to `/dev/kvm` and Landlock (Linux 5.19 or later). Docker Engine is optional: by default jailoc builds images with it when it's running and in a builder VM otherwise. Upgrading an existing installation? See the [upgrade guide](https://seznam.github.io/jailoc/how-to/migrate-from-v1/).

### Homebrew

On macOS with Apple Silicon (the cask works on Linux too). The binary isn't notarized by Apple, so the cask removes its quarantine attribute to keep Gatekeeper from blocking it:

```bash
brew install --cask seznam/tap/jailoc
```

Then run `jailoc` in your project directory; [Getting Started](https://seznam.github.io/jailoc/tutorials/getting-started/) walks through the first run.

Other ways to install:

- <a id="pre-built-binaries"></a>**Pre-built binaries** for macOS arm64 and Linux amd64/arm64: [download an archive](https://seznam.github.io/jailoc/how-to/installation/#install-a-pre-built-binary).
- <a id="nix"></a>**Nix**: `nix run github:seznam/jailoc`, or add the flake; on Linux it builds the VM runtime from source, on aarch64-darwin you provide one. See [Install with Nix](https://seznam.github.io/jailoc/how-to/installation/#install-with-nix).
- <a id="build-from-source"></a>**From source**: `go install …@latest` can't start workspaces; build from a checkout and provide the VM runtime as in [Build from source](https://seznam.github.io/jailoc/how-to/installation/#build-from-source).

## 🛠️ Development

```bash
go generate ./internal/guestbin   # embed the jailoc-guest VM init
go build ./cmd/jailoc
go test ./...
```

[CONTRIBUTING.md](CONTRIBUTING.md) covers VM integration tests, linting and commit style; [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) sets the community guidelines.

## 📦 What's in the default image

Ubuntu 26.04 with Node.js, Python 3.14, npm, four language servers (TypeScript, Pyright, YAML, Bash) and everyday CLI tools such as `rg`, `fdfind`, `jq`, `git`, `gpg`, `ssh` and `curl`. jailoc adds the workspace's coding agent and, with `enable_docker`, Docker on top. Versions are pinned in the [embedded Dockerfile](internal/embed/assets/Dockerfile); the [default image reference](https://seznam.github.io/jailoc/reference/default-image/) lists every tool.

## Security

Report vulnerabilities privately; see [SECURITY.md](SECURITY.md). The [security boundaries](https://seznam.github.io/jailoc/explanation/security-boundaries/) page describes what jailoc isolates and what it doesn't.

## License

jailoc is [MIT](LICENSE)-licensed. Release binaries embed a VM runtime with Apache-2.0, LGPL-2.1 and GPL-2.0 components (libkrunfw bundles a Linux kernel); see `THIRD_PARTY_NOTICES` in each release archive and the [runtime licenses](https://seznam.github.io/jailoc/how-to/installation/#check-the-runtime-licenses). The libkrunfw source is attached to every release as `jailoc_<version>_vm-runtime-source.tar`.
