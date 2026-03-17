# ── Stage 1: build ──────────────────────────────────────────────
FROM ubuntu:24.04 AS builder
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates wget gnupg2 unzip \
    build-essential make gcc g++ python3 \
    && rm -rf /var/lib/apt/lists/*

# Node 22 (needed for npm install -g with native addons)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Bun
ENV BUN_INSTALL="/usr/local/bun"
RUN curl -fsSL https://bun.sh/install | bash

# Go 1.24.1
RUN GOARCH=$(dpkg --print-architecture) \
    && wget -q https://go.dev/dl/go1.24.1.linux-${GOARCH}.tar.gz \
    && tar -C /usr/local -xzf go1.24.1.linux-${GOARCH}.tar.gz \
    && rm go1.24.1.linux-${GOARCH}.tar.gz

# LSP servers
RUN npm install -g yaml-language-server
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/jsonnet-language-server \
    https://github.com/grafana/jsonnet-language-server/releases/download/v0.17.0/jsonnet-language-server_0.17.0_linux_${ARCH} \
    && chmod +x /usr/local/bin/jsonnet-language-server
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/helm_ls \
    https://github.com/mrjosh/helm-ls/releases/download/v0.2.1/helm_ls_linux_${ARCH} \
    && chmod +x /usr/local/bin/helm_ls

# oh-my-openagent (installs platform binary via postinstall)
RUN npm install -g oh-my-openagent@3.11.2

# ── Stage 2: runtime ───────────────────────────────────────────
FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo \
    python3 python3-pip python3-venv \
    ripgrep fd-find jq fzf vim less unzip iptables \
    && rm -rf /var/lib/apt/lists/*

# Node 22 runtime (no build-essential)
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

# Docker CLI + Compose plugin
RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends docker-ce-cli docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

# Copy built artifacts from builder
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /usr/local/bun /usr/local/bun
COPY --from=builder /usr/local/bin/jsonnet-language-server /usr/local/bin/jsonnet-language-server
COPY --from=builder /usr/local/bin/helm_ls /usr/local/bin/helm_ls

# Copy npm global modules (NodeSource uses /usr prefix)
COPY --from=builder /usr/lib/node_modules/yaml-language-server /usr/lib/node_modules/yaml-language-server
COPY --from=builder /usr/lib/node_modules/oh-my-openagent /usr/lib/node_modules/oh-my-openagent

# Link npm global bin stubs (oh-my-openagent JS wrapper has a bug — link platform binary directly)
RUN ln -sf ../lib/node_modules/yaml-language-server/bin/yaml-language-server /usr/bin/yaml-language-server \
    && ARCH=$(uname -m | sed 's/x86_64/x64/' | sed 's/aarch64/arm64/') \
    && ln -sf ../lib/node_modules/oh-my-openagent/node_modules/oh-my-openagent-linux-${ARCH}/bin/oh-my-opencode /usr/bin/oh-my-opencode

RUN corepack enable && corepack prepare yarn@stable --activate

# Entrypoint (runs as root for iptables, drops to agent)
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

# User setup
RUN (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent \
    && chmod 755 /home/agent \
    && mkdir -p /home/agent/.local/state /home/agent/.local/share \
    && chown -R agent:agent /home/agent/.local

# Install opencode as agent user
USER agent
RUN curl -fsSL https://opencode.ai/install | bash -s -- --version 1.2.27
USER root
RUN cp /home/agent/.opencode/bin/opencode /usr/local/bin/opencode && chmod +x /usr/local/bin/opencode

ENV PATH="/home/agent/.opencode/bin:/usr/local/go/bin:/usr/local/bun/bin:${PATH}"

WORKDIR /workspace
EXPOSE 4096
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
