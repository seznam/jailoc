# Default Image Contents

The embedded Dockerfile builds a base image (Ubuntu 26.04) with the tools listed below.

- **Used when** no workspace `image`, `defaults.image` or `[base] dockerfile` is configured: [step 5 of image resolution](image-resolution.md#step-5-embedded-dockerfile).
- **Runs as** the root file system of the workspace VM: jailoc converts its layers into read-only EROFS images in the VM image cache and boots them.
- **Versions**: core tool versions (Node.js, the npm language servers) are pinned as `ARG` declarations at the top of the Dockerfile.
- **No process settings**: the image has no `ENTRYPOINT`, `CMD` or `EXPOSE`. jailoc-guest is the init of the VM and starts sessions over SSH.

---

## Runtimes

| Tool | Package / Source | Binary |
|------|-----------------|--------|
| Node.js | Official tarball | `node`, `npm`, `npx`, `corepack` |
| Python 3.14 | `python3` (apt) | `python3` |

## Language servers

| Server | Install method | Binary |
|--------|---------------|--------|
| typescript-language-server | `npm install -g` | `typescript-language-server` |
| pyright | `npm install -g` | `pyright`, `pyright-langserver` |
| yaml-language-server | `npm install -g` | `yaml-language-server` |
| bash-language-server | `npm install -g` | `bash-language-server` |

## CLI tools

| Tool | Package / Source | Binary |
|------|-----------------|--------|
| ripgrep | `ripgrep` (apt) | `rg` |
| fd | `fd-find` (apt) | `fdfind` |
| jq | `jq` (apt) | `jq` |
| git | `git` (apt) | `git` |
| openssh-client | `openssh-client` (apt) | `ssh` |
| gpg | `gpg` (apt) | `gpg` |
| curl | `curl` (apt) | `curl` |
| sudo | `sudo` (apt, classic `sudo.ws`, installed with `--no-install-recommends`) | `sudo` (`/usr/bin/sudo` through alternatives) |
| unzip | `unzip` (apt) | `unzip` |
| xz-utils | `xz-utils` (apt) | `xz` |

!!! warning
    Python environments that outlive the VM and target the system Python 3.12, such as a `.venv` in a shared project directory or one under the persisted `~/.cache`, must be recreated for Python 3.14.

## Docker layer

The base image contains no Docker. For workspaces with `enable_docker = true` (off by default), jailoc adds a separate layer; see [Image Resolution](image-resolution.md#docker-layer-steps-2-4-and-5).

- Docker CE static binaries, checksum-verified.
- `nftables`, the firewall backend of `dockerd`.
- jailoc-guest starts `dockerd` from these binaries inside the workspace VM.
- Versions are pinned in the embedded [`docker.Dockerfile`](https://github.com/seznam/jailoc/blob/main/internal/embed/assets/docker.Dockerfile).

| Tool | Source | Binary |
|------|--------|--------|
| Docker CE | `download.docker.com` static release | `docker`, `dockerd`, `containerd`, `runc`, `docker-init`, `docker-proxy` in `/usr/local/bin` |
| Docker Buildx | `docker/buildx` GitHub release | `docker buildx` (CLI plugin) |
| Docker Compose | `docker/compose` GitHub release | `docker compose` (CLI plugin) |

## Desktop layer

The base image contains no desktop and no browser. For workspaces with `enable_desktop = true` (off by default), jailoc adds a separate layer of stock `apt` packages: TigerVNC (`Xtigervnc`), Openbox, D-Bus, noVNC and websockify, plus `xterm`, fonts and small X tools; see [Image Resolution](image-resolution.md#desktop-layer-steps-2-4-and-5). The agent installs a browser itself; see [Desktop and Web](../how-to/desktop-and-web.md#run-a-browser-in-the-desktop).

## Coding agent

The base image contains no coding agent. jailoc installs the workspace's agent (OpenCode by default) in a separate layer on top of it; see [Coding agents](configuration.md#coding-agents) and [Image Resolution](image-resolution.md#agent-layer-steps-2-4-and-5).

## User and environment

| Item | Value |
|------|-------|
| Agent user | `agent`, UID 1000, home `/home/agent`, shell `/bin/bash` |
| sudo | passwordless for `agent` (disabled at runtime by `protect_git`) |
| `PATH` | `/home/agent/.local/bin` prepended |
| coreutils | Rust uutils; GNU `cp`, `df`, `mv`, `rm` and `true` |

## System components

| Tool | Purpose |
|------|---------|
| `ca-certificates` | TLS certificate store; a forwarded `ca_bundle` is added to it |
| `libatomic1` | Required by the Node.js runtime |

---

## Related pages

- [Image Resolution](image-resolution.md): the cascade that selects which image to use
- [Custom Images](../how-to/custom-images.md): overlay Dockerfiles that extend the base
- [Overlay Compatibility](overlay-compatibility.md): which image settings the VM uses
