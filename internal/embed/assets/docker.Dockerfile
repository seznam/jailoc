# =============================================================================
# jailoc Docker layer
#
# Docker CE static binaries with the buildx and compose plugins, plus the
# nftables firewall backend dockerd uses in the VM. jailoc adds this layer to
# the images it builds for workspaces with enable_docker; jailoc-guest starts
# dockerd from these binaries. jailoc appends the final stage, which copies
# /out onto the workspace base image.
# =============================================================================

ARG DOCKER_VERSION=29.9.2
ARG BUILDX_VERSION=0.38.1
ARG COMPOSE_VERSION=5.6.1

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
