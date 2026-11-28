# How to use a custom image

By default, jailoc builds the workspace image from the Dockerfile embedded in the binary, exports it and boots the workspace VM from it as its root file system. Replace or extend it with the settings below; the [Image resolution reference](../reference/image-resolution.md) has the full rules.

A workspace `image` is used directly, without an overlay; without one, the base comes from `[defaults] image`, then `[base] dockerfile`, then the embedded Dockerfile, and a workspace `dockerfile` extends that base.

## Know what the image must provide

The image is a root file system, not a container. jailoc ignores its `ENTRYPOINT`, `CMD`, `EXPOSE` and `USER`; `jailoc-guest` is the VM init and starts every session itself. Overlay Dockerfiles that extend the jailoc base (`ARG BASE` / `FROM ${BASE}`) inherit everything below.

An image used directly (`image`) or built from a custom base Dockerfile (`[base] dockerfile`) needs:

- a user `agent` with UID 1000 and home directory `/home/agent`;
- `PATH` including `/home/agent/.local/bin`, and for OpenCode `/home/agent/.opencode/bin`;
- the agent binary (for example `opencode` or `claude`) on `PATH` when you use `image`; `npm` on `PATH` when jailoc installs the [coding agent](coding-agents.md) on top of a `[base] dockerfile` (Antigravity CLI: `curl`, `tar` and `sha256sum` on a glibc system);
- `node` on `PATH` for the [workspace indicator](tui-plugin.md) of Claude Code; without it Claude Code runs without the indicator;
- for [Docker inside the VM](workspace-configuration.md#use-docker-inside-the-vm): `dockerd` and `nftables` with `image`, or `apt-get` (or `nft` on `PATH`) when jailoc adds its Docker layer on top of a `[base] dockerfile`;
- `mkfs.ext4` and `resize2fs` on `PATH` (package `e2fsprogs`; the embedded base image has it) for the [state and cache disks](../reference/configuration.md#persistent-state), which every workspace gets, and for `tmp_storage = "disk"`. Without `mkfs.ext4` the VM fails to start with an error naming it; without `resize2fs` a raised `state_disk` or `cache_disk` is not applied to the file system. jailoc doesn't install either. Add `e2fsprogs` to your base or overlay Dockerfile (see [Set the size, storage and expiry of /tmp](workspace-configuration.md#set-the-size-storage-and-expiry-of-tmp)).

!!! warning
    A plain OS image like `ubuntu:26.04` doesn't meet these requirements. Start from the [jailoc base](../reference/default-image.md) with an overlay unless you need full control.

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

Set `image` in `[defaults]` to start every workspace from a pre-built image. Workspaces with their own `dockerfile` get it as the `BASE` build argument; workspaces without one use it as is:

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
    If the file doesn't exist, the download fails or exceeds 1 MiB, or the build fails, `jailoc up` aborts. There's no fallback to the embedded image. A local file must stay out of read-write shares; see [Keep build inputs out of read-write shares](#keep-build-inputs-out-of-read-write-shares).

## Add a workspace layer

Set `dockerfile` on the workspace. jailoc builds it on top of the resolved base, passes the base tag as `BASE` and tags the result `jailoc-myproject:<content-hash>`. HTTP(S) URLs work here too.

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/.config/jailoc/images/myproject/overlay.Dockerfile"
```

The Dockerfile must start with `ARG BASE` and `FROM ${BASE}`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

Packages installed with `sudo apt` inside a running VM are lost at the next start. Put lasting tools in this Dockerfile.

## Set the build context

For a local `dockerfile`, the build context defaults to its parent directory. Set `build_context` to choose which files `COPY` can see:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/.config/jailoc/images/myproject/Dockerfile"
build_context = "~/.config/jailoc/images/myproject"
```

The context is the directory `build_context` points to, whole; `.dockerignore` does not narrow what jailoc checks. A context with more than 500,000 entries is refused, so point `build_context` at a smaller directory.

## Keep build inputs out of read-write shares

The local Dockerfiles and the whole build context are build inputs. jailoc refuses to start a workspace when a read-write share (`paths`, read-write `mounts`, profile state shares) of that workspace or of a running workspace exposes one, and when its own read-write shares expose the build inputs of any other configured workspace. There is no opt-out. A project root that is a workspace path cannot be the build context.

Keep the Dockerfile and the files it `COPY`s in a directory outside every workspace's read-write `paths` and `mounts`:

```toml
[workspaces.myproject]
paths = ["~/projects/myproject"]
dockerfile = "~/.config/jailoc/images/myproject/Dockerfile"
```

Alternatives:

- mount the directory read-only (`~/projects/myproject/docker:/home/agent/docker:ro`), which is allowed;
- use a URL `dockerfile` without `build_context`, which reads nothing local;
- use a prebuilt [`image`](#use-a-pre-built-image).

When the check fails, see [Fix a refused start because of build inputs](troubleshooting.md#workspace-refuses-to-start-because-of-build-inputs-in-read-write-shares).

## Choose where images are built

!!! warning
    With the host Docker daemon, `RUN` steps run on the host daemon, not inside a VM, and reach every network the host reaches. Use `builder = "vm"` or `isolated_build` for Dockerfiles you don't trust.

`[vm] builder` selects the build engine:

| Value | Builds with |
|-------|-------------|
| `"auto"` (default) | the host Docker daemon when it answers and builds Linux images for the host's CPU architecture, otherwise the builder VM |
| `"docker"` | the host Docker daemon; `jailoc up` fails when it isn't usable, or when a [CA bundle](#build-with-a-private-ca) is selected |
| `"vm"` | always the builder VM |

```toml
[vm]
builder = "vm"
```

The shared builder VM serves all workspaces, keeps one build cache and reaches public addresses only, regardless of `allowed_hosts` and `allowed_networks`. For a builder VM of the workspace's own, with its own cache and the workspace's network rules, set `isolated_build`. It always uses a builder VM, whatever `[vm] builder` says:

```toml
[workspaces.myproject]
isolated_build = true
```

Images that pull from private registries need `isolated_build = true` with the hosts in `allowed_hosts`, or `builder = "docker"` (not available with a [CA bundle](#build-with-a-private-ca)).

## Build with a private CA

When [`ca_bundle`](../reference/configuration.md#ca_bundle) selects an external bundle, your builds trust it with no change to your Dockerfiles: no `COPY` of the certificate, no build flags, no TLS variables. Set it to a path, or leave it at the default and export `SSL_CERT_FILE` (or `NIX_SSL_CERT_FILE`) on the host:

```toml
[defaults]
ca_bundle = "~/certs/company-ca.pem"
```

The next image build (`jailoc up` of a stopped workspace, or `jailoc restart`) then runs every image, including each stage of your Dockerfiles, in a builder VM, and the tools in your `RUN` steps (`apt`, `curl`, `git`, `uv`, Node.js and `npm`) trust the bundle on Debian and Ubuntu. The host Docker daemon and its trust settings are left alone.

- With `builder = "docker"`, the build fails before it starts, also with `isolated_build`. Set `builder = "auto"` or `"vm"`.
- Every stage must be Debian or Ubuntu with a shell. A stage jailoc can't set up fails the build with an error, as do classic builders and custom Dockerfile frontends.
- Base images and `COPY --from` images from other registries must be available from a registry; an image that exists only in the host Docker daemon makes the build fail. Push it to a registry.
- Builds that need a private network need `isolated_build = true` with the hosts in `allowed_hosts` or `allowed_networks`; the shared builder VM reaches public addresses only.
- jailoc adds client variables (`SSL_CERT_FILE` and similar) only where the base image and your Dockerfile set none. A variable you set wins, so make it point at `/etc/ssl/certs/ca-certificates.crt` or remove it. A client that pins its own roots can't be fixed by jailoc.

!!! warning
    The certificate stays in the built image. Changing or disabling `ca_bundle` rebuilds the affected images at the next image build, but `jailoc reload` doesn't rebuild them: it reports that `jailoc restart` is required and leaves the VM and its sessions running. Until the restart, the image keeps the old trust. The [reference](../reference/configuration.md#image-builds-with-a-ca-bundle) has the full rules.

## Apply an image change

Images are built only at `jailoc up` of a stopped workspace and at `jailoc restart`. After changing `dockerfile`, `image` or the base, run:

```bash
jailoc restart myproject
```

`jailoc reload` and `jailoc up` on a running workspace never rebuild. When the running VM's read-write shares expose build inputs they print a `WARNING`, and the affected builds are refused until you change the shares and restart the workspace. See [Keep build inputs out of read-write shares](#keep-build-inputs-out-of-read-write-shares).

## Write a good overlay Dockerfile

### Keep layers small and cache-friendly

Order instructions from least to most volatile, combine related `RUN` commands into one layer, and skip package manager caches in the same layer:

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
