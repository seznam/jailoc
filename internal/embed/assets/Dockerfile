# =============================================================================
# jailoc base image
#
# Core sandbox image for jailoc workspaces. Provides the runtime environment
# for headless coding agents: Node.js, language servers, CLI tools, a
# non-root "agent" user (UID 1000). jailoc installs the workspace's agent
# (OpenCode, Claude Code, ...) in a separate layer on top of this image. The
# VM guest manages the agent process and the network broker enforces
# isolation outside the image.
#
# Preset/overlay images (e.g. jailoc-szn-preset) extend this with additional
# language runtimes and tooling via `FROM <this-image>`.
#
# Multi-stage build: "builder" compiles language servers and npm modules,
# final stage copies only the resulting binaries to keep the image small.
# =============================================================================

ARG NODE_VERSION=26.5.0
ARG YAML_LS_VERSION=1.24.0
ARG TS_LS_VERSION=5.3.0
ARG TS_VERSION=6.0.3
ARG PYRIGHT_VERSION=1.1.411
ARG BASH_LS_VERSION=5.6.0
ARG DOCKER_VERSION=29.9.2
ARG BUILDX_VERSION=0.38.1
ARG COMPOSE_VERSION=5.6.1

# =============================================================================
# Builder stage — compile language servers and install npm modules.
# None of the build-time packages (gcc, make, etc.) end up in the final image.
# =============================================================================
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive
# Build-time dependencies: compilers for native npm addons (node-gyp),
# curl for downloading tarballs, xz-utils for .tar.xz extraction.
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates xz-utils \
    build-essential make gcc g++ python3 \
    && rm -rf /var/lib/apt/lists/*

# Node.js — needed here to run `npm install` for language servers below.
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
RUN npm install -g \
    yaml-language-server@${YAML_LS_VERSION} \
    typescript-language-server@${TS_LS_VERSION} \
    typescript@${TS_VERSION} \
    pyright@${PYRIGHT_VERSION} \
    bash-language-server@${BASH_LS_VERSION}

# =============================================================================
# Docker stage — Docker CE static binaries with the buildx and compose
# plugins, checksum-verified. The VM guest runs dockerd from these binaries.
# =============================================================================
FROM ubuntu:24.04 AS docker

RUN apt-get update && apt-get install -y --no-install-recommends curl ca-certificates \
    && rm -rf /var/lib/apt/lists/*
ARG DOCKER_VERSION
ARG BUILDX_VERSION
ARG COMPOSE_VERSION
RUN set -eu; \
    case "$(dpkg --print-architecture)" in \
      arm64) arch=aarch64 goarch=arm64 \
        docker_sha=873b3e0207857c3b2f28392fb922f3ea6efe37620c16d4d4aaf2fef409c90b73 \
        buildx_sha=3b83b3731916ba433feccaaa689fe5bcdc81c641d92c8e6c2ea865dfa141c97a \
        compose_sha=42cfa75f51ad0ccf290bad37899e0038a8c10a0069e85233ecc1d53e3ff1347d ;; \
      amd64) arch=x86_64 goarch=amd64 \
        docker_sha=9ac225999fd5e863f7694c728ef23d480616b1e5639328fa309befd4243d69eb \
        buildx_sha=e25d5cd675092bb5d1d03632fdc46cdf3fede7b514dc5f023a105e00d3ce5ad4 \
        compose_sha=47737f4236e6d3ccea7291995203f8f018058783b4c3a9469119811a0b5db8c0 ;; \
      *) echo "unsupported architecture" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/docker.tgz "https://download.docker.com/linux/static/stable/${arch}/docker-${DOCKER_VERSION}.tgz"; \
    echo "${docker_sha}  /tmp/docker.tgz" | sha256sum -c -; \
    mkdir -p /out/bin /out/cli-plugins; \
    tar -xzf /tmp/docker.tgz -C /out/bin --strip-components=1; \
    rm /out/bin/ctr; \
    curl -fsSL -o /out/cli-plugins/docker-buildx "https://github.com/docker/buildx/releases/download/v${BUILDX_VERSION}/buildx-v${BUILDX_VERSION}.linux-${goarch}"; \
    echo "${buildx_sha}  /out/cli-plugins/docker-buildx" | sha256sum -c -; \
    curl -fsSL -o /out/cli-plugins/docker-compose "https://github.com/docker/compose/releases/download/v${COMPOSE_VERSION}/docker-compose-linux-${arch}"; \
    echo "${compose_sha}  /out/cli-plugins/docker-compose" | sha256sum -c -; \
    chmod 0755 /out/bin/* /out/cli-plugins/*

# =============================================================================
# Final stage — minimal runtime image with only what the agent needs.
# =============================================================================
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
# Runtime packages:
#   curl, ca-certificates  — HTTPS fetches (uv install, package downloads)
#   git, openssh-client    — repo operations (clone, push over SSH)
#   sudo                   — agent user has passwordless sudo for apt installs
#   python3                — Python runtime for pyright and user scripts
#   ripgrep (rg), fd-find, jq — fast search/filter tools used by coding agents
#   unzip, xz-utils        — archive extraction for runtime downloads
#   libatomic1             — required by Node.js 26+ runtime
#   nftables               — firewall backend of dockerd in VM workspaces
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo \
    python3 ripgrep fd-find jq unzip xz-utils libatomic1 nftables \
    && rm -rf /var/lib/apt/lists/*

# Docker CLI with buildx and compose, plus dockerd for VM workspaces.
COPY --from=docker /out/bin/ /usr/local/bin/
COPY --from=docker /out/cli-plugins/ /usr/local/lib/docker/cli-plugins/

# Node.js — installed again in the final stage (not copied from builder)
# because the builder's /usr/local is polluted with build-time files.
# Needed at runtime for npm-based language servers and corepack (Yarn).
ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Copy npm global modules — these were `npm install -g`'d in builder.
# Copied as directories because npm global modules are not standalone binaries.
COPY --from=builder /usr/local/lib/node_modules/yaml-language-server /usr/local/lib/node_modules/yaml-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript-language-server /usr/local/lib/node_modules/typescript-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript /usr/local/lib/node_modules/typescript
COPY --from=builder /usr/local/lib/node_modules/pyright /usr/local/lib/node_modules/pyright
COPY --from=builder /usr/local/lib/node_modules/bash-language-server /usr/local/lib/node_modules/bash-language-server

# Symlink npm module entry points into /usr/local/bin so they're on PATH.
# Required because COPY doesn't preserve the symlinks `npm install -g` creates.
RUN ln -sf /usr/local/lib/node_modules/yaml-language-server/bin/yaml-language-server /usr/local/bin/yaml-language-server \
    && ln -sf /usr/local/lib/node_modules/typescript-language-server/lib/cli.mjs /usr/local/bin/typescript-language-server \
    && ln -sf /usr/local/lib/node_modules/pyright/index.js /usr/local/bin/pyright \
    && ln -sf /usr/local/lib/node_modules/pyright/langserver.index.js /usr/local/bin/pyright-langserver \
    && ln -sf /usr/local/lib/node_modules/bash-language-server/out/cli.js /usr/local/bin/bash-language-server

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
