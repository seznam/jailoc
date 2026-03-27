# Default Image Contents

The embedded Dockerfile builds a base image (Ubuntu 24.04) with the tools listed below. This is the image used when no custom `image`, `defaults.image`, or `base.dockerfile` is configured — [step 5 of image resolution](image-resolution.md#step-5-embedded-fallback).

Core tool versions (Node.js, yaml-language-server, OpenCode) are pinned as `ARG` declarations at the top of the Dockerfile and tracked by Renovate. Other npm-installed language servers use the latest version at build time.

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

## Agent stack

| Tool | Source | Binary |
|------|--------|--------|
| OpenCode | `opencode.ai/install` | `opencode` |

## System components

These are installed for container operation, not direct agent use.

| Tool | Purpose |
|------|---------|
| `iptables` | Network isolation (entrypoint.sh) |
| `ca-certificates` | TLS certificate store |

---

## Related pages

- [Image Resolution](image-resolution.md) — the 5-step cascade that selects which image to use
- [Custom Images](../how-to/custom-images.md) — how to build overlay Dockerfiles that extend the base
- [Overlay Compatibility](overlay-compatibility.md) — which Dockerfile instructions survive into the final container
