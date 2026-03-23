# renovate: datasource=golang-version depName=go versioning=semver
ARG GO_VERSION=1.26.1
# renovate: datasource=node-version depName=node versioning=node
ARG NODE_VERSION=24.14.0
# renovate: datasource=github-releases depName=oven-sh/bun extractVersion=^bun-v(?<version>.*)$
ARG BUN_VERSION=1.3.11
# renovate: datasource=npm depName=oh-my-openagent
ARG OMOA_VERSION=3.12.3
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
# renovate: datasource=npm depName=yarn
ARG YARN_VERSION=4.13.0

FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates wget gnupg2 unzip xz-utils \
    build-essential make gcc g++ python3 \
    && rm -rf /var/lib/apt/lists/*

# Node
ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Bun
ARG BUN_VERSION
ENV BUN_INSTALL="/usr/local/bun"
RUN curl -fsSL https://bun.sh/install | bash -s "bun-v${BUN_VERSION}"

# Go
ARG GO_VERSION
RUN GOARCH=$(dpkg --print-architecture) \
    && wget -q https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz \
    && tar -C /usr/local -xzf go${GO_VERSION}.linux-${GOARCH}.tar.gz \
    && rm go${GO_VERSION}.linux-${GOARCH}.tar.gz

ENV GOPATH=/root/go
ENV PATH="/usr/local/go/bin:${GOPATH}/bin:${PATH}"

ARG YAML_LS_VERSION
RUN npm install -g \
    yaml-language-server@${YAML_LS_VERSION} \
    typescript-language-server \
    typescript \
    pyright \
    bash-language-server

ARG GOPLS_VERSION
RUN go install golang.org/x/tools/gopls@latest

ARG JSONNET_LS_VERSION
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/jsonnet-language-server \
      https://github.com/grafana/jsonnet-language-server/releases/download/${JSONNET_LS_VERSION}/jsonnet-language-server_${JSONNET_LS_VERSION#v}_linux_${ARCH} \
    && chmod +x /usr/local/bin/jsonnet-language-server

ARG HELM_LS_VERSION
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/helm_ls \
      https://github.com/mrjosh/helm-ls/releases/download/${HELM_LS_VERSION}/helm_ls_linux_${ARCH} \
    && chmod +x /usr/local/bin/helm_ls

# oh-my-openagent (installs platform binary via postinstall)
ARG OMOA_VERSION
RUN npm install -g oh-my-openagent@${OMOA_VERSION}

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates gnupg2 git openssh-client sudo \
    python3 python3-pip python3-venv \
    ripgrep fd-find jq fzf vim less unzip xz-utils iptables \
    && rm -rf /var/lib/apt/lists/*

ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends docker-ce-cli docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

# Copy built artifacts from builder
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /usr/local/bun /usr/local/bun
COPY --from=builder /usr/local/bin/jsonnet-language-server /usr/local/bin/jsonnet-language-server
COPY --from=builder /usr/local/bin/helm_ls /usr/local/bin/helm_ls
COPY --from=builder /root/go/bin/gopls /usr/local/bin/gopls

# Copy npm global modules (official Node tarball uses /usr/local prefix)
COPY --from=builder /usr/local/lib/node_modules/yaml-language-server /usr/local/lib/node_modules/yaml-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript-language-server /usr/local/lib/node_modules/typescript-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript /usr/local/lib/node_modules/typescript
COPY --from=builder /usr/local/lib/node_modules/pyright /usr/local/lib/node_modules/pyright
COPY --from=builder /usr/local/lib/node_modules/bash-language-server /usr/local/lib/node_modules/bash-language-server
COPY --from=builder /usr/local/lib/node_modules/oh-my-openagent /usr/local/lib/node_modules/oh-my-openagent

# Link npm global bin stubs (oh-my-openagent JS wrapper has a bug — link platform binary directly)
RUN ln -sf /usr/local/lib/node_modules/yaml-language-server/bin/yaml-language-server /usr/local/bin/yaml-language-server \
    && ln -sf /usr/local/lib/node_modules/typescript-language-server/lib/cli.mjs /usr/local/bin/typescript-language-server \
    && ln -sf /usr/local/lib/node_modules/pyright/index.js /usr/local/bin/pyright \
    && ln -sf /usr/local/lib/node_modules/pyright/langserver.index.js /usr/local/bin/pyright-langserver \
    && ln -sf /usr/local/lib/node_modules/bash-language-server/out/cli.js /usr/local/bin/bash-language-server \
    && ARCH=$(uname -m | sed 's/x86_64/x64/' | sed 's/aarch64/arm64/') \
    && ln -sf /usr/local/lib/node_modules/oh-my-openagent/node_modules/oh-my-openagent-linux-${ARCH}/bin/oh-my-opencode /usr/local/bin/oh-my-opencode

# Yarn
ARG YARN_VERSION
RUN corepack enable && corepack prepare yarn@${YARN_VERSION} --activate

# User setup
RUN (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent \
    && chmod 755 /home/agent \
    && mkdir -p /home/agent/.local/state /home/agent/.local/share \
    && chown -R agent:agent /home/agent/.local

USER agent
RUN curl -LsSf https://astral.sh/uv/install.sh | sh
USER root

RUN mkdir -p /home/linuxbrew \
    && chown -R agent:agent /home/linuxbrew \
    && su - agent -c 'NONINTERACTIVE=1 bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"' \
    && chown -R agent:agent /home/linuxbrew/.linuxbrew

# Install opencode as agent user
ARG OPENCODE_VERSION
USER agent
RUN curl -fsSL https://opencode.ai/install | bash -s -- --version ${OPENCODE_VERSION#v}
USER root
RUN cp /home/agent/.opencode/bin/opencode /usr/local/bin/opencode && chmod +x /usr/local/bin/opencode

# Entrypoint (runs as root for iptables, drops to agent)
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV PATH="/home/agent/.local/bin:/home/agent/.opencode/bin:/usr/local/go/bin:/usr/local/bun/bin:/home/linuxbrew/.linuxbrew/bin:/home/linuxbrew/.linuxbrew/sbin:${PATH}"

EXPOSE 4096
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
