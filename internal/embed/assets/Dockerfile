# =============================================================================
# jailoc base image
#
# Core sandbox image for jailoc workspaces. Provides the runtime environment
# for headless coding agents: Node.js, language servers, CLI tools, a
# non-root "agent" user (UID 1000). jailoc installs the workspace's agent
# (OpenCode, Claude Code, ...) and, with enable_docker, Docker in separate
# layers on top of this image. The
# VM guest manages the agent process and the network broker enforces
# isolation outside the image.
#
# Preset/overlay images (e.g. jailoc-szn-preset) extend this with additional
# language runtimes and tooling via `FROM <this-image>`.
#
# Single stage: none of the npm language servers below ship native addons, so
# no compiler toolchain is needed. apt and npm downloads use BuildKit cache
# mounts; for the classic builder jailoc strips the mounts and sets
# JAILOC_CLEAN_CACHES so the RUN steps clean up after themselves.
# =============================================================================

ARG NODE_VERSION=26.5.0
ARG YAML_LS_VERSION=1.24.0
ARG TS_LS_VERSION=5.3.0
ARG TS_VERSION=6.0.3
ARG PYRIGHT_VERSION=1.1.411
ARG BASH_LS_VERSION=5.6.0

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
ARG JAILOC_CLEAN_CACHES
# Runtime packages:
#   curl, ca-certificates  — HTTPS fetches (uv install, package downloads)
#   git, openssh-client    — repo operations (clone, push over SSH)
#   sudo                   — agent user has passwordless sudo for apt installs
#   python3                — Python runtime for pyright and user scripts
#   ripgrep (rg), fd-find, jq — fast search/filter tools used by coding agents
#   unzip, xz-utils        — archive extraction for runtime downloads
#   libatomic1             — required by Node.js 26+ runtime
# docker-clean is moved aside during the install so downloaded packages stay
# in the cache mount, and restored so the image behaves like stock Ubuntu.
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt/lists,sharing=locked \
    mv /etc/apt/apt.conf.d/docker-clean /etc/apt/docker-clean \
    && apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo \
    python3 ripgrep fd-find jq unzip xz-utils libatomic1 \
    && mv /etc/apt/docker-clean /etc/apt/apt.conf.d/docker-clean \
    && if [ -n "$JAILOC_CLEAN_CACHES" ]; then apt-get clean && rm -rf /var/lib/apt/lists/*; fi

# Node.js — runtime for npm-based language servers and corepack (Yarn).
# Installed from the official tarball (not apt) for version control.
ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Language servers (npm) — coding agents use these for code intelligence
# (go-to-definition, diagnostics, completions) across languages.
ARG YAML_LS_VERSION
ARG TS_LS_VERSION
ARG TS_VERSION
ARG PYRIGHT_VERSION
ARG BASH_LS_VERSION
RUN --mount=type=cache,target=/root/.npm \
    npm install -g --cache /root/.npm \
    yaml-language-server@${YAML_LS_VERSION} \
    typescript-language-server@${TS_LS_VERSION} \
    typescript@${TS_VERSION} \
    pyright@${PYRIGHT_VERSION} \
    bash-language-server@${BASH_LS_VERSION} \
    && if [ -n "$JAILOC_CLEAN_CACHES" ]; then rm -rf /root/.npm; fi

# Agent user (UID 1000) — the non-root identity the agent process runs as.
# UID 1000 matches the default host user for bind-mount permission compatibility.
# Passwordless sudo allows the agent to install packages when needed.
RUN (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent \
    && chmod 755 /home/agent \
    && mkdir -p /home/agent/.local/state /home/agent/.local/share \
    && chown -R agent:agent /home/agent/.local

# PATH includes agent's local bin dirs for tools installed as the agent user
# (e.g. uv from preset overlays goes to ~/.local/bin).
ENV PATH="/home/agent/.local/bin:${PATH}"
