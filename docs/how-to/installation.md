# How to install jailoc

This guide covers every installation method and the prerequisites you need before running jailoc.

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

## Install a pre-built binary

Download the archive for your platform from the table below, then extract and move the binary to a directory on your `PATH`.

| Platform | Architecture | Download |
|----------|-------------|---------|
| Linux    | amd64       | [jailoc_linux_amd64.tar.gz](../downloads/jailoc_linux_amd64.tar.gz) |
| Linux    | arm64       | [jailoc_linux_arm64.tar.gz](../downloads/jailoc_linux_arm64.tar.gz) |
| macOS    | amd64       | [jailoc_darwin_amd64.tar.gz](../downloads/jailoc_darwin_amd64.tar.gz) |
| macOS    | arm64       | [jailoc_darwin_arm64.tar.gz](../downloads/jailoc_darwin_arm64.tar.gz) |

Extract, make executable, and place it on your `PATH`:

```bash
tar -xzf jailoc_linux_amd64.tar.gz
chmod +x jailoc
sudo mv jailoc /usr/local/bin/
```

Adjust the archive name to match your platform and architecture.

---

## Verify the checksum

Download [checksums.txt](../downloads/checksums.txt) alongside the archive, then verify:

```bash
sha256sum -c checksums.txt
```

You should see `jailoc_linux_amd64.tar.gz: OK` (or the equivalent for your archive). Any `FAILED` line means the download is corrupt or tampered with.

!!! warning
    Do not run a binary that fails checksum verification.
