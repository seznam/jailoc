# 🐳 Custom Images

There are three levels of image customization:

**1. Workspace-specific layer** — create `~/.config/jailoc/{name}.Dockerfile`. This file is built on top of the resolved base image using `ARG BASE`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc passes the base image tag as `--build-arg BASE=...` and tags the result `jailoc-{name}:latest`.

**2. Full base override** — create `~/.config/jailoc/Dockerfile`. This replaces the entire base image. jailoc builds it as `jailoc-base:local` and uses it instead of pulling from the registry. Use this if you need to completely swap out the base.

**3. Default behavior (no custom files)** — jailoc pulls the versioned image from the configured registry. If the pull fails, it falls back to an embedded Dockerfile baked into the binary and builds `jailoc-base:embedded` locally.

The workspace layer (step 1) is always applied on top of whatever base was resolved.
