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

FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo \
    python3 ripgrep fd-find jq unzip xz-utils iptables \
    && rm -rf /var/lib/apt/lists/*

ARG NODE_VERSION
RUN ARCH=$(dpkg --print-architecture | sed 's/amd64/x64/') \
    && curl -fsSL https://nodejs.org/dist/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${ARCH}.tar.xz \
    | tar -xJC /usr/local --strip-components=1

# Copy built artifacts from builder
COPY --from=builder /usr/local/bin/jsonnet-language-server /usr/local/bin/jsonnet-language-server
COPY --from=builder /usr/local/bin/helm_ls /usr/local/bin/helm_ls
COPY --from=builder /root/go/bin/gopls /usr/local/bin/gopls

# Copy npm global modules (official Node tarball uses /usr/local prefix)
COPY --from=builder /usr/local/lib/node_modules/yaml-language-server /usr/local/lib/node_modules/yaml-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript-language-server /usr/local/lib/node_modules/typescript-language-server
COPY --from=builder /usr/local/lib/node_modules/typescript /usr/local/lib/node_modules/typescript
COPY --from=builder /usr/local/lib/node_modules/pyright /usr/local/lib/node_modules/pyright
COPY --from=builder /usr/local/lib/node_modules/bash-language-server /usr/local/lib/node_modules/bash-language-server

# Link npm global bin stubs
RUN ln -sf /usr/local/lib/node_modules/yaml-language-server/bin/yaml-language-server /usr/local/bin/yaml-language-server \
    && ln -sf /usr/local/lib/node_modules/typescript-language-server/lib/cli.mjs /usr/local/bin/typescript-language-server \
    && ln -sf /usr/local/lib/node_modules/pyright/index.js /usr/local/bin/pyright \
    && ln -sf /usr/local/lib/node_modules/pyright/langserver.index.js /usr/local/bin/pyright-langserver \
    && ln -sf /usr/local/lib/node_modules/bash-language-server/out/cli.js /usr/local/bin/bash-language-server

# User setup
RUN (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent \
    && chmod 755 /home/agent \
    && mkdir -p /home/agent/.local/state /home/agent/.local/share \
    && chown -R agent:agent /home/agent/.local

# Install opencode as agent user
ARG OPENCODE_VERSION
USER agent
RUN curl -fsSL https://opencode.ai/install | bash -s -- --version ${OPENCODE_VERSION#v}
USER root
RUN cp /home/agent/.opencode/bin/opencode /usr/local/bin/opencode && chmod +x /usr/local/bin/opencode

# Entrypoint (runs as root for iptables, drops to agent)
COPY entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENV PATH="/home/agent/.local/bin:/home/agent/.opencode/bin:${PATH}"

EXPOSE 4096
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
