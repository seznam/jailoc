# How to install jailoc

This guide covers the supported platforms, prerequisites and installation methods.

## Check your platform

Every workspace runs in its own libkrun micro-VM. jailoc supports two host platforms:

| Platform | Hypervisor | Requirement |
|----------|------------|-------------|
| macOS on Apple Silicon (`darwin/arm64`) | Hypervisor.framework | None |
| Linux on x86-64 (`linux/amd64`) | KVM | Read and write access to `/dev/kvm` |

On Linux, check that KVM is available and that your user can open it:

```bash
ls -l /dev/kvm
```

If the device belongs to the `kvm` group, add yourself to it and log in again:

```bash
sudo usermod -aG kvm "$USER"
```

Other platforms are unsupported. If you build the [go-microvm](https://github.com/stacklok/go-microvm) runner and libkrun yourself, point jailoc at them in the [`[vm]` section](../reference/configuration.md):

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/libkrun/lib"
```

## Know when Docker is needed

The workspace VMs don't use Docker. You need Docker Engine on the host only for:

- building workspace images when `[vm] builder` is `"docker"`, or `"auto"` with a usable daemon (see [How to use a custom image](custom-images.md));
- `jailoc migrate`, which copies OpenCode data out of jailoc v1 Docker volumes (see [Migrate from jailoc v1](migrate-from-v1.md)).

Without a usable host daemon, `"auto"` builds images in a builder VM.

## Install with go install

```bash
go install github.com/seznam/jailoc/cmd/jailoc@{{ version }}
```

The binary lands in `$GOPATH/bin` (or `$HOME/go/bin` by default). Make sure that directory is on your `PATH`.

## Install a pre-built binary

Pre-built archives for macOS (arm64) and Linux (amd64) are published with every release. Download the one matching your platform from the [GitHub Releases page](https://github.com/seznam/jailoc/releases), extract it, and place the binary on your `PATH`.

## Let jailoc fetch the VM runtime

You don't install the VM runtime by hand. The first `jailoc up` downloads the pinned runtime for your platform (the `go-microvm-runner` process, libkrun and the libkrunfw guest kernel), verifies its SHA-256 checksums and unpacks it into `~/.cache/jailoc-vm/runtime/`. Later starts reuse it. If the download or the checksum check fails, see [Troubleshooting](troubleshooting.md#vm-runtime-download-fails).

!!! note
    A jailoc built from source without `go generate ./internal/guestbin` has no embedded `jailoc-guest` and looks for it next to the runner instead. See [Development](../development.md).
