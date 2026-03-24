# How to use a custom Docker image

By default, jailoc pulls a versioned base image from the configured registry. This guide shows how to replace or extend that image at each level of customization. For the full resolution rules, see [Image resolution reference](../reference/image-resolution.md).

---

## Use a local Dockerfile as the base image

Set `dockerfile` in the global `[image]` section to an absolute path on your host. jailoc reads the file, builds it locally, and tags the result with a content-based hash (`jailoc-base:preset-<hash>`).

```toml
[image]
dockerfile = "/opt/myorg/base.Dockerfile"
```

Tilde paths work too:

```toml
[image]
dockerfile = "~/dockerfiles/base.Dockerfile"
```

!!! warning
    If the file doesn't exist or the build fails, jailoc aborts. There is no fallback to the registry or embedded image.

---

## Use a remote Dockerfile URL as the base image

Set `dockerfile` in `[image]` to an HTTP(S) URL. jailoc downloads the file, builds it locally, and tags the result with a content-based hash.

```toml
[image]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/opencode.Dockerfile"
```

!!! warning
    If the download fails or exceeds 1 MiB, jailoc aborts. The URL must be reachable at `jailoc up` time.

---

## Add a workspace-specific layer

Set `dockerfile` in a `[workspaces.<name>]` block. jailoc builds this Dockerfile on top of whatever base image was resolved by the `[image]` settings, passing the base tag as a build argument.

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
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/myproject-overlay.Dockerfile"
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

## Default behavior (no customization)

When no `dockerfile` is configured, jailoc:

1. Pulls the versioned image from the registry configured in `[image].repository`.
2. If the pull fails, builds from the Dockerfile embedded in the jailoc binary itself, tagging the result `jailoc-base:embedded`.

You don't need to do anything to get this behavior. It's the starting point before any of the customization steps above.
