# =============================================================================
# jailoc base image
#
# Core sandbox image for jailoc workspaces. Provides the runtime environment
# for headless OpenCode coding agents: Node.js, language servers, CLI tools,
# a non-root "agent" user (UID 1000), and an entrypoint that configures
# network isolation via iptables before dropping privileges.
#
# Preset/overlay images (e.g. jailoc-szn-preset) extend this with additional
# language runtimes and tooling via `FROM <this-image>`.
#
# Multi-stage build: "builder" compiles language servers and npm modules,
# final stage copies only the resulting binaries to keep the image small.
# =============================================================================

# Pinned versions — Renovate comments tell the bot which datasource to poll
# for automated version-bump PRs.
# renovate: datasource=golang-version depName=go versioning=semver
ARG GO_VERSION=1.26.1
# renovate: datasource=node-version depName=node versioning=node
ARG NODE_VERSION=24.14.0
# renovate: datasource=npm depName=yaml-language-server
ARG YAML_LS_VERSION=1.21.0
# renovate: datasource=github-releases depName=golang/tools extractVersion=^gopls/v(?<version>.*)$
ARG GOPLS_VERSION=0.21.1
# renovate: datasource=github-releases depName=grafana/jsonnet-language-server
ARG JSONNET_LS_VERSION=v0.17.0
# renovate: datasource=github-releases depName=mrjosh/helm-ls
ARG HELM_LS_VERSION=v0.5.4
# renovate: datasource=github-releases depName=sst/opencode
ARG OPENCODE_VERSION=v1.2.27

# =============================================================================
# Builder stage — compile language servers and install npm modules.
# None of the build-time packages (gcc, make, etc.) end up in the final image.
# =============================================================================
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive
# Build-time dependencies: compilers for native npm addons (node-gyp),
# curl/wget for downloading tarballs, xz-utils for .tar.xz extraction.
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates wget gnupg2 unzip xz-utils \
    build-essential make gcc g++ python3 \
    && rm -rf /var/lib/apt/lists/*

# Node.js — needed here to run `npm install` for language servers below.
# Installed from the official tarball (not apt) for version control.
ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Go — needed here only to `go install gopls`. The Go toolchain is NOT copied
# to the final image; preset overlays bring their own Go if workspaces need it.
ARG GO_VERSION
RUN GOARCH=$(dpkg --print-architecture) \
    && wget -q https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz \
    && tar -C /usr/local -xzf go${GO_VERSION}.linux-${GOARCH}.tar.gz \
    && rm go${GO_VERSION}.linux-${GOARCH}.tar.gz

ENV GOPATH=/root/go
ENV PATH="/usr/local/go/bin:${GOPATH}/bin:${PATH}"

# Language servers (npm) — OpenCode uses these for code intelligence
# (go-to-definition, diagnostics, completions) across languages.
ARG YAML_LS_VERSION
RUN npm install -g \
    yaml-language-server@${YAML_LS_VERSION} \
    typescript-language-server \
    typescript \
    pyright \
    bash-language-server

# gopls — Go language server, compiled from source via `go install`.
ARG GOPLS_VERSION
RUN go install golang.org/x/tools/gopls@latest

# jsonnet-language-server — pre-built binary from GitHub Releases.
ARG JSONNET_LS_VERSION
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/jsonnet-language-server \
      https://github.com/grafana/jsonnet-language-server/releases/download/${JSONNET_LS_VERSION}/jsonnet-language-server_${JSONNET_LS_VERSION#v}_linux_${ARCH} \
    && chmod +x /usr/local/bin/jsonnet-language-server

# helm-ls — Helm chart language server, pre-built binary from GitHub Releases.
ARG HELM_LS_VERSION
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/helm_ls \
      https://github.com/mrjosh/helm-ls/releases/download/${HELM_LS_VERSION}/helm_ls_linux_${ARCH} \
    && chmod +x /usr/local/bin/helm_ls

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
#   ripgrep, fd-find, jq   — fast search/filter tools used by OpenCode
#   unzip, xz-utils        — archive extraction for runtime downloads
#   iptables               — network isolation: entrypoint.sh blocks RFC 1918
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo \
    python3 ripgrep fd-find jq unzip xz-utils iptables \
    && rm -rf /var/lib/apt/lists/*

# Node.js — installed again in the final stage (not copied from builder)
# because the builder's /usr/local is polluted with build-time files.
# Needed at runtime for npm-based language servers and corepack (Yarn).
ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Copy only the compiled binaries from builder — no compilers, no source.
COPY --from=builder /usr/local/bin/jsonnet-language-server /usr/local/bin/jsonnet-language-server
COPY --from=builder /usr/local/bin/helm_ls /usr/local/bin/helm_ls
COPY --from=builder /root/go/bin/gopls /usr/local/bin/gopls

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
# entrypoint.sh drops privileges from root to this user via setpriv.
# UID 1000 matches the default host user for bind-mount permission compatibility.
# Passwordless sudo allows the agent to install packages when needed.
RUN (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent \
    && chmod 755 /home/agent \
    && mkdir -p /home/agent/.local/state /home/agent/.local/share \
    && chown -R agent:agent /home/agent/.local

# OpenCode — the AI coding agent. Installed as `agent` user so it writes to
# /home/agent/.opencode/. Copied to /usr/local/bin for global PATH access.
ARG OPENCODE_VERSION
USER agent
RUN curl -fsSL https://opencode.ai/install | bash -s -- --version ${OPENCODE_VERSION#v}
USER root
RUN cp /home/agent/.opencode/bin/opencode /usr/local/bin/opencode && chmod +x /usr/local/bin/opencode

# Entrypoint — runs as root on container start to:
#   1. Configure iptables (allow DinD sidecar + gateway + allowed hosts,
#      block RFC 1918 / link-local / CGNAT private networks)
#   2. chown data directories
#   3. Drop privileges to agent (UID 1000) via setpriv --no-new-privs
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# PATH includes agent's local bin dirs for tools installed as the agent user
# (e.g. uv from preset overlays goes to ~/.local/bin).
ENV PATH="/home/agent/.local/bin:/home/agent/.opencode/bin:${PATH}"

# Default OpenCode serve port — jailoc assigns ports starting at 4096.
EXPOSE 4096
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
