# How to install jailoc

This guide covers installation methods and the prerequisites you need before running jailoc.

## Prerequisites

- **Docker Engine** must be running on your machine. jailoc communicates with the Docker daemon directly.
- No `docker compose` CLI plugin is required. jailoc embeds the Compose SDK and manages containers without it.
- The container runtime's Linux kernel must support **netfilter** (iptables). jailoc uses iptables rules inside the container to enforce [network isolation](../explanation/network-isolation.md). Runtimes whose kernel lacks netfilter support cannot run jailoc.

### Docker runtime compatibility

| Runtime | Platform | Status | Notes |
|---------|----------|--------|-------|
| Docker Engine | Linux | ✅ | Native performance, no VM overhead |
| OrbStack | macOS | ✅ | Lightweight VM, fast file I/O — recommended on macOS |
| Docker Desktop | macOS / Linux | ✅ | VirtioFS file sharing; higher memory footprint than OrbStack |
| Colima | macOS | ✅ | Lima-based VM; performance depends on VM type (`vz` faster than `qemu`) |
| Podman | macOS | ✅ | VM-based on macOS; comparable to Docker Desktop |
| Rancher Desktop (VZ + Rosetta) | macOS | ✅ | Rosetta provides a more complete kernel with netfilter support |
| Rancher Desktop (VZ, no Rosetta) | macOS | ❌ | VZ hypervisor without Rosetta runs a minimal ARM64 kernel that lacks netfilter — jailoc probes both `iptables-nft` and `iptables-legacy` but neither works, so startup is aborted |
| Docker Engine (rootless) | Linux | ⚠️ | Untested — DinD sidecar requires `--privileged`, which rootless mode may not support |
| WSL2 + Docker | Windows | ⚠️ | Untested — the Linux binary may work under WSL2 with Docker Engine installed inside the distribution |

jailoc connects to whichever Docker daemon your current **docker context** points to. If your runtime uses a non-default socket path (common with Colima or Podman), make sure the active context is set correctly:

```bash
# list available contexts
docker context ls

# switch to a specific runtime
docker context use colima
```

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
