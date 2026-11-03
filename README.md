![jailoc](docs/hero.jpeg)

# jailoc

[![CI](https://github.com/seznam/jailoc/actions/workflows/ci.yml/badge.svg)](https://github.com/seznam/jailoc/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/seznam/jailoc)](https://github.com/seznam/jailoc/releases/latest)

Run coding agents (OpenCode, Claude Code, GitHub Copilot CLI, OmO) in sandboxed micro-VMs.

📖 **[Full documentation](https://seznam.github.io/jailoc/)**

## What is this?

`jailoc` runs coding agents such as OpenCode, Claude Code, GitHub Copilot CLI and OmO in isolated libkrun micro-VMs so they can work autonomously, isolated from your host system except for the directories you share. Each workspace boots its own VM with a network policy that blocks private networks by default, letting you control exactly which internal services the agent can reach. You configure which directories to share, which hosts to allowlist, and the agent runs inside the VM in detachable sessions you attach to over SSH.

## Why jailoc

- 📁 **File isolation**: each workspace is a separate VM with its own kernel. The agent only sees directories you explicitly share and files you copy in. SSH keys, browser profiles, and other projects are invisible.
- 🌐 **Network isolation**: all VM traffic goes through a host-side network broker that blocks private and reserved IPv4 and IPv6 ranges by default. You allowlist only what the agent needs. No pivoting to internal infrastructure.
- 🐳 **Sandboxed Docker**: opt in with `enable_docker = true` and the Docker daemon runs inside the workspace VM. No host socket mounting.
- ⚡ **Zero config to start**: `jailoc` unpacks its embedded VM runtime, builds the image, boots the VM and attaches you to the agent.

## Installation

**Requirements:** macOS on Apple Silicon, or Linux amd64 (arm64 experimental) with KVM (`/dev/kvm` access) and Landlock (Linux 5.19 or later). Docker Engine must be running on the host to build workspace images. Upgrading from jailoc v1? Run `jailoc migrate`.

### Homebrew

On macOS and Linux:

```bash
brew install --cask seznam/tap/jailoc
```

The binary is not notarized by Apple; the cask removes the quarantine attribute so Gatekeeper does not block it.

### Build from source

`go install …@latest` produces a binary without the embedded VM init (`jailoc-guest`), so build from a checkout:

```bash
git clone https://github.com/seznam/jailoc.git && cd jailoc
go generate ./internal/guestbin
go install ./cmd/jailoc
```

Make sure `$GOPATH/bin` (default `$HOME/go/bin`) is on your `PATH`. Source builds don't embed the VM runtime; set `[vm] runner` and `lib_dir` or build the runtime first, see [installation](https://seznam.github.io/jailoc/how-to/installation/#build-from-source).

### Nix

Run without installing:

```bash
nix run github:seznam/jailoc
```

Or add to your flake inputs:

```nix
{
  inputs.jailoc.url = "github:seznam/jailoc";
}
```

and include the package in your `environment.systemPackages` or `home.packages`:

```nix
inputs.jailoc.packages."${pkgs.stdenv.hostPlatform.system}".jailoc
```

The Nix package is a source build, so it doesn't embed the VM runtime either: set `[vm] runner` and `lib_dir` as described in [installation](https://seznam.github.io/jailoc/how-to/installation/#build-from-source).

### Pre-built binaries

Download the archive for your platform from [GitHub Releases](https://github.com/seznam/jailoc/releases) (macOS arm64, Linux amd64/arm64), extract, and place the `jailoc` binary on your `PATH`.

## 🛠️ Development

```bash
# Build from source (embeds the jailoc-guest VM init)
go generate ./internal/guestbin
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run VM integration tests (requires KVM or macOS and a go-microvm runtime)
JAILOC_VM_RUNNER=/path/to/go-microvm-runner JAILOC_VM_LIBDIR=/path/to/libs \
  go test -tags vmintegration -count=1 -timeout 50m ./internal/vmintegration/
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) for community guidelines.

## 📦 What's in the default image

The default base image (Ubuntu 24.04) ships with:

| Category | Tools |
|----------|-------|
| Runtimes | Node.js, Python 3 |
| Package managers | npm |
| Language servers | typescript-language-server, pyright, yaml-language-server, bash-language-server |
| CLI tools | rg (ripgrep), fdfind, jq, git, openssh-client, curl, sudo |

jailoc installs the workspace's coding agent and, with `enable_docker`, Docker on top of it. Exact versions are pinned in the [embedded Dockerfile](internal/embed/assets/Dockerfile). See the [default image reference](https://seznam.github.io/jailoc/reference/default-image/) for the full list of installed tools.

## Security

Report vulnerabilities privately; see [SECURITY.md](SECURITY.md). The [security boundaries](https://seznam.github.io/jailoc/explanation/security-boundaries/) page describes what jailoc isolates and what it doesn't.

## License

jailoc is [MIT](LICENSE)-licensed. Release binaries embed a VM runtime with Apache-2.0, LGPL-2.1 and GPL-2.0 components (libkrunfw bundles a Linux kernel); see `THIRD_PARTY_NOTICES` in each release archive and the [runtime licenses](https://seznam.github.io/jailoc/how-to/installation/#check-the-runtime-licenses). The libkrunfw source is attached to every release as `jailoc_<version>_vm-runtime-source.tar`.
