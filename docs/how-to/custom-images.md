# How to use a custom image

By default, jailoc builds the workspace image from the Dockerfile embedded in the binary, exports it and boots the workspace VM from it as its root file system. This guide shows how to replace or extend that image. For the full resolution rules, see the [Image resolution reference](../reference/image-resolution.md).

## Know what the image must provide

The image is a root file system, not a container. jailoc ignores its `ENTRYPOINT`, `CMD`, `EXPOSE` and `USER`; `jailoc-guest` is the VM init and starts every session itself. Overlay Dockerfiles that extend the jailoc base (`ARG BASE` / `FROM ${BASE}`) inherit everything below.

An image used directly (`image`) or built from a custom base Dockerfile (`[base] dockerfile`) needs:

- a user `agent` with UID 1000 and home directory `/home/agent`;
- `npm` on `PATH` when jailoc installs the [coding agent](coding-agents.md) on top of a `[base] dockerfile`, or the agent binary (for example `opencode` or `claude`) on `PATH` in an image used directly;
- `PATH` including `/home/agent/.local/bin`, and for OpenCode `/home/agent/.opencode/bin`;
- for Docker inside the VM, `apt-get` (or `nft` on `PATH`) when jailoc adds its Docker layer on top of a `[base] dockerfile`, or `dockerd` and `nftables` in an image used directly (see [Use Docker inside the VM](workspace-configuration.md#use-docker-inside-the-vm)).

!!! warning
    A plain OS image like `ubuntu:24.04` doesn't meet these requirements. Start from the jailoc base with an overlay unless you need full control.

See the [Overlay compatibility reference](../reference/overlay-compatibility.md) and the [Default image reference](../reference/default-image.md).

## Use a pre-built image

Set `image` to skip the build of the workspace layer:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
image = "myregistry.example.com/myteam/myapp:v1.2.3"
```

!!! warning
    `image` can't be combined with `dockerfile` or `build_context` in the same workspace. Setting both is a validation error.

## Set a default base image

Set `image` in `[defaults]` to start every workspace from a pre-built image. Workspaces with their own `dockerfile` get it as the `BASE` build argument; workspaces without one use it as-is:

```toml
[defaults]
image = "myregistry.example.com/myteam/agent-base:v1.2.3"
```

## Build the base from your own Dockerfile

Set `dockerfile` in `[base]` to a local path or HTTP(S) URL. jailoc reads or downloads it, builds it and tags the result with a content hash (`jailoc-base:preset-<hash>`):

```toml
[base]
dockerfile = "~/dockerfiles/base.Dockerfile"
# or
# dockerfile = "https://git.example.com/team/dockerfiles/-/raw/main/opencode.Dockerfile"
```

!!! warning
    If the file doesn't exist, the download fails or exceeds 1 MiB, or the build fails, `jailoc up` aborts. There's no fallback to the embedded image.

## Add a workspace layer

Set `dockerfile` on the workspace. jailoc builds it on top of the resolved base and passes the base tag as `BASE`:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/projects/myproject/overlay.Dockerfile"
```

The Dockerfile must start with:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

The result is tagged `jailoc-myproject:<content-hash>`. HTTP(S) URLs work here too.

Packages installed with `sudo apt` inside a running VM are lost at the next start. Put lasting tools in this Dockerfile.

## Set the build context

For a local `dockerfile`, the build context defaults to its parent directory. Set `build_context` to choose which files `COPY` can see:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/projects/myproject/docker/overlay.Dockerfile"
build_context = "~/projects/myproject"
```

## Choose where images are built

`[vm] builder` selects the build engine:

| Value | Builds with |
|-------|-------------|
| `"auto"` (default) | the host Docker daemon when it answers and builds Linux images for the host's CPU architecture, otherwise the builder VM |
| `"docker"` | the host Docker daemon; `jailoc up` fails when it isn't usable |
| `"vm"` | always the builder VM |

```toml
[vm]
builder = "vm"
```

!!! warning
    With the host Docker daemon, `RUN` steps run on the host daemon, not inside a VM, and reach every network the host reaches. Use `builder = "vm"` or `isolated_build` for Dockerfiles you don't trust.

The shared builder VM serves all workspaces, keeps one build cache and reaches public addresses only, regardless of `allowed_hosts` and `allowed_networks`. To build in a builder VM of the workspace's own, with its own cache and the workspace's network rules, set:

```toml
[workspaces.myproject]
isolated_build = true
```

`isolated_build` always uses a builder VM, whatever `[vm] builder` says. Images that pull from private registries need `isolated_build = true` with the hosts in `allowed_hosts`, or `builder = "docker"`.

## Apply an image change

Images are built only at `jailoc up` of a stopped workspace and at `jailoc restart`. After changing `dockerfile`, `image` or the base, run:

```bash
jailoc restart myproject
```

## Write a good overlay Dockerfile

### Keep layers small and cache-friendly

Order instructions from least to most volatile, and combine related `RUN` commands into one layer:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client \
    redis-tools \
    build-essential \
    && rm -rf /var/lib/apt/lists/*
```

Skip package manager caches in the same layer:

```dockerfile
RUN pip install --no-cache-dir -r requirements.txt
RUN npm ci --prefer-offline && npm cache clean --force
```

### Use multi-stage builds for compiled tools

Build a binary in a separate stage and copy only the output:

```dockerfile
ARG BASE
FROM golang:1.24 AS builder
WORKDIR /src
COPY tool/ .
RUN CGO_ENABLED=0 go build -o /bin/mytool .

FROM ${BASE}
COPY --from=builder /bin/mytool /usr/local/bin/mytool
```

### Install a release binary

```dockerfile
ARG BASE
FROM ${BASE}

RUN curl -fsSL https://github.com/org/tool/releases/download/v1.0/tool-linux-amd64.tar.gz \
    | tar -xzf - -C /usr/local/bin tool
```

### Use a proxy during builds

jailoc forwards the host's `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, `FTP_PROXY`, `ALL_PROXY` and their lowercase variants as build arguments to every image build. No `ARG` declarations are needed; Docker treats them as [predefined build arguments](https://docs.docker.com/build/building/variables/#proxy-arguments).

### Avoid these changes

!!! danger "Fatal"
    - **Deleting `/home/agent` or the UID 1000 `agent` user**: sessions run as this user and fail to start without it.
    - **Removing the agent binary (for example `opencode`)**: sessions have nothing to run.

!!! warning "Breaking"
    - **Overriding `ENV PATH` without `/home/agent/.local/bin` and `/home/agent/.opencode/bin`**: OpenCode and its plugins aren't found.
    - **Removing `sudo`**: the agent can't become root inside the VM.
