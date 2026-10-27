# Default Image Contents

The embedded Dockerfile builds a base image (Ubuntu 24.04) with the tools listed below. This image is used when no workspace `image`, `defaults.image` or `[base] dockerfile` is configured: [step 5 of image resolution](image-resolution.md#step-5-embedded-dockerfile). jailoc unpacks it into the VM image cache and boots it as the root file system of the workspace VM.

Core tool versions (Node.js, the npm language servers, OpenCode, Docker, Buildx, Compose) are pinned as `ARG` declarations at the top of the Dockerfile.

The image has no `ENTRYPOINT`, `CMD` or `EXPOSE`. jailoc-guest is the init of the VM and starts sessions over SSH.

---

## Runtimes

| Tool | Package / Source | Binary |
|------|-----------------|--------|
| Node.js | Official tarball | `node`, `npm`, `npx`, `corepack` |
| Python 3 | `python3` (apt) | `python3` |

## Language servers

| Server | Install method | Binary |
|--------|---------------|--------|
| typescript-language-server | `npm install -g` (builder stage) | `typescript-language-server` |
| pyright | `npm install -g` (builder stage) | `pyright`, `pyright-langserver` |
| yaml-language-server | `npm install -g` (builder stage) | `yaml-language-server` |
| bash-language-server | `npm install -g` (builder stage) | `bash-language-server` |

## CLI tools

| Tool | Package / Source | Binary |
|------|-----------------|--------|
| ripgrep | `ripgrep` (apt) | `rg` |
| fd | `fd-find` (apt) | `fdfind` |
| jq | `jq` (apt) | `jq` |
| git | `git` (apt) | `git` |
| openssh-client | `openssh-client` (apt) | `ssh` |
| curl | `curl` (apt) | `curl` |
| sudo | `sudo` (apt) | `sudo` |
| unzip | `unzip` (apt) | `unzip` |
| xz-utils | `xz-utils` (apt) | `xz` |

## Docker

Docker CE static binaries, checksum-verified. With `enable_docker = true`, jailoc-guest starts `dockerd` from these binaries inside the workspace VM.

| Tool | Source | Binary |
|------|--------|--------|
| Docker CE | `download.docker.com` static release | `docker`, `dockerd`, `containerd`, `runc`, `docker-init`, `docker-proxy` in `/usr/local/bin` |
| Docker Buildx | `docker/buildx` GitHub release | `docker buildx` (CLI plugin) |
| Docker Compose | `docker/compose` GitHub release | `docker compose` (CLI plugin) |

## Agent stack

| Tool | Source | Binary |
|------|--------|--------|
| OpenCode | npm package `opencode-ai` | `opencode` |

## User and environment

| Item | Value |
|------|-------|
| Agent user | `agent`, UID 1000, home `/home/agent`, shell `/bin/bash` |
| sudo | passwordless for `agent` (disabled at runtime by `protect_git`) |
| `PATH` | `/home/agent/.local/bin` prepended |

## System components

| Tool | Purpose |
|------|---------|
| `nftables` | Firewall backend of `dockerd` inside the VM |
| `ca-certificates` | TLS certificate store; a forwarded `ca_bundle` is added to it |
| `libatomic1` | Required by the Node.js runtime |

---

## Related pages

- [Image Resolution](image-resolution.md): the cascade that selects which image to use
- [Custom Images](../how-to/custom-images.md): overlay Dockerfiles that extend the base
- [Overlay Compatibility](overlay-compatibility.md): which image settings the VM uses
