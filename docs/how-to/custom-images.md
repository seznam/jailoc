# How to use a custom Docker image

By default, jailoc pulls a versioned base image from the configured registry. This guide shows how to replace or extend that image at each level of customization. For the full resolution order, see [Image resolution reference](../reference/image-resolution.md).

---

## Use a remote Dockerfile URL

The highest-priority option. Set `dockerfile` in the global `[image]` section or on a specific workspace. jailoc downloads the file over HTTP(S), builds it locally, and tags the result with a content-based hash (`jailoc-base:preset-<hash>`).

**Global override** (applies to all workspaces unless a workspace sets its own):

```toml
[image]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/opencode.Dockerfile"
```

**Per-workspace override** (takes priority over the global setting):

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/myproject.Dockerfile"
```

!!! warning
    If the download fails, jailoc aborts. There is no fallback. The URL must be reachable at `jailoc up` time and the file must not exceed 1 MiB.

---

## Add a workspace-specific layer

Create a file named `~/.config/jailoc/{workspace-name}.Dockerfile`. jailoc builds this on top of whatever base image was resolved, passing the base tag as a build argument.

For a workspace named `myproject`, create `~/.config/jailoc/myproject.Dockerfile`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc runs the build with `--build-arg BASE=<resolved-base-tag>` and tags the result as `jailoc-myproject:latest`. This layer always applies on top of whatever base was resolved by the other steps.

---

## Replace the entire base image

Create `~/.config/jailoc/Dockerfile` (without any workspace prefix). jailoc builds it as `jailoc-base:local` and uses it as the base for all workspaces that don't have a remote `dockerfile` set.

```dockerfile
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    curl git \
    && rm -rf /var/lib/apt/lists/*
```

!!! note
    A workspace-specific `{name}.Dockerfile` will still be applied on top of this image, just as with the registry-pulled base.

---

## Default behavior (no customization)

When no `dockerfile` is configured and no local `Dockerfile` exists, jailoc:

1. Pulls the versioned image from the configured registry.
2. If the pull fails, builds from the Dockerfile embedded in the jailoc binary itself, tagging the result `jailoc-base:embedded`.

You don't need to do anything to get this behavior. It's the starting point before any of the customization steps above.
