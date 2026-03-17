FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
    curl ca-certificates git openssh-client sudo wget gnupg2 \
    build-essential make gcc g++ \
    python3 python3-pip python3-venv \
    ripgrep fd-find jq fzf vim less unzip \
    && rm -rf /var/lib/apt/lists/*

RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
    && apt-get install -y nodejs \
    && rm -rf /var/lib/apt/lists/*

ENV BUN_INSTALL="/usr/local/bun"
RUN curl -fsSL https://bun.sh/install | bash
ENV PATH="/usr/local/bun/bin:${PATH}"
RUN corepack enable && corepack prepare yarn@stable --activate

RUN GOARCH=$(dpkg --print-architecture) \
    && wget -q https://go.dev/dl/go1.24.1.linux-${GOARCH}.tar.gz \
    && tar -C /usr/local -xzf go1.24.1.linux-${GOARCH}.tar.gz \
    && rm go1.24.1.linux-${GOARCH}.tar.gz
ENV PATH="/usr/local/go/bin:${PATH}"

# yaml-language-server via npm
RUN npm install -g yaml-language-server

# jsonnet-language-server v0.17.0 from Grafana GitHub releases
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/jsonnet-language-server \
    https://github.com/grafana/jsonnet-language-server/releases/download/v0.17.0/jsonnet-language-server_0.17.0_linux_${ARCH} \
    && chmod +x /usr/local/bin/jsonnet-language-server

# helm_ls from mrjosh GitHub releases (pinned to v0.2.1)
RUN ARCH=$(dpkg --print-architecture) \
    && curl -Lo /usr/local/bin/helm_ls \
    https://github.com/mrjosh/helm-ls/releases/download/v0.2.1/helm_ls_linux_${ARCH} \
    && chmod +x /usr/local/bin/helm_ls

RUN curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg \
    && echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list \
    && apt-get update && apt-get install -y --no-install-recommends docker-ce-cli docker-compose-plugin \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd -g 998 docker || true \
    && (userdel -r ubuntu || true) \
    && useradd -m -s /bin/bash -u 1000 -G docker,root agent \
    && echo "agent ALL=(ALL) NOPASSWD: ALL" > /etc/sudoers.d/agent
USER agent

RUN curl -fsSL https://opencode.ai/install | bash -s -- --version 1.2.27
RUN sudo cp /home/agent/.opencode/bin/opencode /usr/local/bin/opencode && sudo chmod +x /usr/local/bin/opencode
ENV PATH="/home/agent/.opencode/bin:${PATH}"
RUN bunx oh-my-opencode install --no-tui --claude=no --openai=no --gemini=no --copilot=yes --opencode-zen=no --zai-coding-plan=no --kimi-for-coding=no --skip-auth

WORKDIR /workspace
EXPOSE 4096
CMD ["opencode", "serve", "--hostname", "0.0.0.0", "--port", "4096"]
