# 📥 Downloads

Pre-built binaries are published for each release via [GoReleaser](https://goreleaser.com/).
Download the binary for your platform, make it executable, and place it on your `PATH`.

## 🗂️ Pre-built Binaries

| Platform | Architecture | Download |
|----------|-------------|---------|
| Linux | amd64 | [jailoc_linux_amd64.tar.gz](downloads/jailoc_linux_amd64.tar.gz) |
| Linux | arm64 | [jailoc_linux_arm64.tar.gz](downloads/jailoc_linux_arm64.tar.gz) |
| macOS | amd64 | [jailoc_darwin_amd64.tar.gz](downloads/jailoc_darwin_amd64.tar.gz) |
| macOS | arm64 | [jailoc_darwin_arm64.tar.gz](downloads/jailoc_darwin_arm64.tar.gz) |

🔐 [checksums.txt](downloads/checksums.txt) — SHA-256 checksums for all archives.

## ⚡ Installation

Extract the binary and place it on your `PATH`:

```bash
tar -xzf jailoc_linux_amd64.tar.gz
chmod +x jailoc
sudo mv jailoc /usr/local/bin/
```

Verify the checksum before installing 🔍:

```bash
sha256sum -c checksums.txt
```

## 📋 Requirements

Requires Docker Engine (the daemon) to be running 🐳.
jailoc embeds the Compose SDK — no `docker compose` CLI plugin needed.
