# How to install jailoc

This guide covers installation methods and the prerequisites you need before running jailoc.

## Prerequisites

- **Docker Engine** must be running on your machine. jailoc communicates with the Docker daemon directly.
- No `docker compose` CLI plugin is required. jailoc embeds the Compose SDK and manages containers without it.

---

## Install with go install

The fastest method if you have a Go toolchain available:

```bash
go install github.com/seznam/jailoc/cmd/jailoc@{{ version }}
```

The binary lands in `$GOPATH/bin` (or `$HOME/go/bin` by default). Make sure that directory is on your `PATH`.

---

## Pre-built binaries

Pre-built archives for Linux and macOS (amd64/arm64) are published with every release. Download the one matching your platform from the [GitHub Releases page](https://github.com/seznam/jailoc/releases), extract, and place the binary on your `PATH`.
