# How to use a custom Docker image

By default, jailoc builds a base image from the Dockerfile embedded in the binary. This guide shows how to replace or extend that image at each level of customization. For the full resolution rules, see [Image resolution reference](../reference/image-resolution.md).

---

## Use a pre-built image directly

Set `image` in the workspace block to skip all build steps. Compose pulls the image from its registry at container startup.

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
image = "myregistry.example.com/myteam/myapp:v1.2.3"
```

!!! warning
    `image` cannot be combined with `dockerfile` or `build_context` in the same workspace block. Setting both is a validation error.

---

## Set a default base image

Set `image` in `[defaults]` to use a pre-built image as the starting point for all workspaces. Workspaces that have their own `dockerfile` will use this as the `BASE` build argument; workspaces without a `dockerfile` will use it directly.

```toml
[defaults]
image = "myregistry.example.com/myteam/opencode-base:v1.2.3"
```

The image must contain OpenCode and the required agent tooling (iptables, setpriv). A plain OS image like `ubuntu:22.04` cannot be used here — the container would fail to start. When `defaults.image` is not set, jailoc builds from its embedded Dockerfile automatically.

With this set, a workspace that adds its own `dockerfile` builds on top of the specified image:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/projects/myproject/overlay.Dockerfile"
```

A workspace without a `dockerfile` receives the specified image as-is.

---

## Use a custom Dockerfile as the base image

Set `dockerfile` in the global `[base]` section to a local path or HTTP(S) URL. jailoc reads (or downloads) the file, builds it locally, and tags the result with a content-based hash (`jailoc-base:preset-<hash>`).

```toml
[base]
dockerfile = "/opt/myorg/base.Dockerfile"
```

Tilde paths and HTTP(S) URLs work too:

```toml
[base]
dockerfile = "~/dockerfiles/base.Dockerfile"
# or
dockerfile = "https://git.example.com/team/dockerfiles/-/raw/main/opencode.Dockerfile"
```

!!! warning
    If the file doesn't exist, the download fails (or exceeds 1 MiB), or the build fails, jailoc aborts. There is no fallback to the embedded image.

---

## Add a workspace-specific layer

Set `dockerfile` in a `[workspaces.<name>]` block. jailoc builds this Dockerfile on top of whatever base image was resolved (from `[base]` settings, or `defaults.image` if set), passing the base tag as a build argument.

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/projects/myproject/overlay.Dockerfile"
```

The workspace Dockerfile must begin with:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc runs the build with `--build-arg BASE=<resolved-base-tag>` and tags the result as `jailoc-myproject:<content-hash>`.

HTTP URLs work here too:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "https://git.example.com/team/dockerfiles/-/raw/main/myproject-overlay.Dockerfile"
```

---

## Set an explicit build context for the workspace overlay

By default, the build context for a workspace overlay is the parent directory of the `dockerfile` (for local paths). Set `build_context` explicitly to control which files are available during the build.

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/projects/myproject/docker/overlay.Dockerfile"
build_context = "~/projects/myproject"
```

With this configuration, files from `~/projects/myproject` are accessible via `COPY` instructions in the Dockerfile.

---

## Write a good overlay Dockerfile

### Keep layers small and cache-friendly

Order instructions from least to most volatile. Put package installs before copying project files, and copy files before running project-specific setup. Build tools and package managers should come last.

Combine related `RUN` commands into a single layer:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client \
    redis-tools \
    build-essential \
    && rm -rf /var/lib/apt/lists/*
```

For pip, suppress the cache directory:

```dockerfile
RUN pip install --no-cache-dir -r requirements.txt
```

For npm, use `--prefer-offline` or clean the cache in the same layer:

```dockerfile
RUN npm ci --prefer-offline && npm cache clean --force
```

For apk (Alpine-derived layers):

```dockerfile
RUN apk add --no-cache curl jq
```

### Use multi-stage builds for compiled tools

If you need a compiled binary, build it in a separate stage and copy only the output:

```dockerfile
ARG BASE
FROM golang:1.24 AS builder
WORKDIR /src
COPY tool/ .
RUN CGO_ENABLED=0 go build -o /bin/mytool .

FROM ${BASE}
COPY --from=builder /bin/mytool /usr/local/bin/mytool
```

This keeps the final image free of compilers and intermediate artifacts.

### Install a custom binary

To add a pre-built binary from a release URL, download and install it in a single `RUN` layer:

```dockerfile
ARG BASE
FROM ${BASE}

RUN curl -fsSL https://github.com/org/tool/releases/download/v1.0/tool-linux-amd64 \
    -o /usr/local/bin/tool \
    && chmod +x /usr/local/bin/tool
```

For archives that need extraction:

```dockerfile
RUN curl -fsSL https://github.com/org/tool/releases/download/v1.0/tool-linux-amd64.tar.gz \
    | tar -xzf - -C /usr/local/bin tool
```

If the tool requires compilation, use a [multi-stage build](#use-multi-stage-builds-for-compiled-tools) instead to keep the final image small.

### What to avoid

!!! danger "Fatal: breaks the container entirely"
    These changes cause the container to fail at startup or make the agent unusable:

    - **Deleting `/home/agent` or removing UID 1000** — the entrypoint drops privileges to UID 1000 and all agent tools run as this user; without it, startup fails.
    - **Removing `iptables`** — the entrypoint uses iptables to enforce network isolation rules; without it, the container exits immediately.
    - **Removing `setpriv`** — the entrypoint calls `setpriv` to drop capabilities and switch to UID 1000; without it, the agent runs as root or the container fails to start.

!!! warning "Breaking: causes silent misbehaviour"
    These changes don't prevent the container from starting, but they break expected behaviour in non-obvious ways:

    - **Overriding `ENV PATH` without including `/home/agent/.local/bin` and `/home/agent/.opencode/bin`** — the agent's local pip tools and opencode plugins won't be found.
    - **Adding `VOLUME` on workspace mount paths** — Docker volumes shadow bind mounts, so your project files won't appear inside the container.

!!! warning "Degraded: reduces functionality"
    - **Removing `sudo`** — the entrypoint uses sudo to set up iptables rules before dropping privileges. Removing it degrades the network isolation setup.

For the full compatibility matrix, see [Overlay compatibility reference](../reference/overlay-compatibility.md).
