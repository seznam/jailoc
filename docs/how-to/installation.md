# How to install jailoc

This guide covers the supported platforms, prerequisites and installation methods.

## Check your platform

Every workspace runs in its own libkrun micro-VM. jailoc supports these host platforms:

| Platform | Hypervisor | Requirement |
|----------|------------|-------------|
| macOS on Apple Silicon (`darwin/arm64`) | Hypervisor.framework | None |
| Linux on x86-64 (`linux/amd64`) | KVM | Read and write access to `/dev/kvm`; Linux 5.19 or later with Landlock enabled |
| Linux on ARM64 (`linux/arm64`) | KVM | Same as `linux/amd64`; experimental, not covered by the VM integration tests |

On Linux, check that KVM is available and that your user can open it:

```bash
ls -l /dev/kvm
```

If the device belongs to the `kvm` group, add yourself to it and log in again:

```bash
sudo usermod -aG kvm "$USER"
```

jailoc confines the VM process with Landlock and refuses to start a VM when the kernel lacks it. Check that `landlock` is listed among the active security modules:

```bash
cat /sys/kernel/security/lsm
```

If it is missing, add `landlock` to the `lsm=` kernel parameter.

!!! note "Linux file ownership"
    On Linux, libkrun's virtio-fs shows the real host owner of every file, so `jailoc-guest` mounts the VM's root filesystem and the shares idmapped: inside the VM your files appear as root's or the agent's, and on the host everything belongs to your user. Root inside the VM cannot give a shared file another host owner. If the guest kernel rejects idmapped mounts, the agent falls back to your user's UID and GID and `sudo` does not grant root.

Other platforms, including Intel Macs, are unsupported. If you build the [go-microvm](https://github.com/stacklok/go-microvm) runner and libkrun yourself, point jailoc at them in the [`[vm]` section](../reference/configuration.md):

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

## Install with Homebrew

On macOS and Linux, install the latest release from the [seznam/homebrew-tap](https://github.com/seznam/homebrew-tap) tap:

```bash
brew install --cask seznam/tap/jailoc
```

Upgrade with `brew upgrade --cask jailoc`. The tap carries stable releases only; install release candidates from the [GitHub Releases page](https://github.com/seznam/jailoc/releases). The cask installs both `jailoc` and the `jl` alias on your `PATH`.

!!! note "Unsigned binary"
    jailoc binaries are not signed or notarized by Apple. The cask removes the `com.apple.quarantine` attribute from `jailoc` after installation so that Gatekeeper does not block it.

## Install a pre-built binary

Pre-built archives for macOS (arm64) and Linux (amd64, arm64) are published with every release. Download the one matching your platform from the [GitHub Releases page](https://github.com/seznam/jailoc/releases), extract it, and place the binary on your `PATH`.

## Build from source

`go install github.com/seznam/jailoc/cmd/jailoc@...` produces a binary without `jailoc-guest`, the VM init, and cannot start workspaces. Build from a checkout instead, embedding the guest binary with `go generate`:

```bash
git clone https://github.com/seznam/jailoc.git
cd jailoc
go generate ./internal/guestbin
go install ./cmd/jailoc
```

The binary lands in `$GOPATH/bin` (or `$HOME/go/bin` by default). Make sure that directory is on your `PATH`.

A source build doesn't include the VM runtime (the `jailoc-vm-runner` process, libkrun and the libkrunfw guest kernel) and fails to start workspaces with `this jailoc build does not include the VM runtime`. Either set `runner` and `lib_dir` in the [`[vm]` section](../reference/configuration.md) as shown in [Check your platform](#check-your-platform), or build the runtime into `internal/vm/runtimebin/<os>-<arch>/` before `go install` with `scripts/vm-runtime/build-init.sh` followed by `scripts/vm-runtime/build-runtime-linux.sh` or `scripts/vm-runtime/build-runtime-darwin.sh` (see the comments in each script for its requirements).

## Know where the VM runtime lives

Release binaries embed the VM runtime for their platform. The first `jailoc up` unpacks it into `~/.cache/jailoc-vm/runtime/`; later starts reuse it. Nothing is downloaded.

### Check the runtime licenses

jailoc itself is MIT-licensed. The embedded runtime carries its own licenses:

| Component | License | Notes |
|-----------|---------|-------|
| `jailoc-vm-runner` | Apache-2.0 | Built from [go-microvm](https://github.com/stacklok/go-microvm); runs as a separate process |
| libkrun | Apache-2.0 | Built with the patches in `scripts/vm-runtime/libkrun-patches/` |
| musl libc | MIT | Statically linked into libkrun's guest init |
| libkrunfw | LGPL-2.1-only (library), GPL-2.0-only (bundled Linux kernel) | |

Every release archive contains `THIRD_PARTY_NOTICES` with the license texts of all bundled Go modules, Rust crates and runtime libraries. Each unpacked runtime directory contains `LICENSE`, `NOTICE`, the libkrunfw license texts and a `SOURCE` file.

The complete corresponding source of libkrunfw, including the Linux kernel tarball and the build scripts, is attached to every [GitHub Release](https://github.com/seznam/jailoc/releases) as `jailoc_<version>_vm-runtime-source.tar`.

Every release also has SPDX SBOMs, generated by [syft](https://github.com/anchore/syft): `<archive>.sbom.json` lists the Go modules in jailoc, and `jailoc_<version>_vm-runtime_<os>_<arch>.sbom.json` lists the Go modules in the embedded `jailoc-vm-runner` and the Rust crates in libkrun (built with [cargo-auditable](https://github.com/rust-secure-code/cargo-auditable)). syft can't detect libkrunfw, so `THIRD_PARTY_NOTICES` remains the complete list.

Release assets carry [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations). To check that a file was built by the jailoc release workflow:

```bash
gh attestation verify jailoc_<version>_linux_amd64.tar.gz --repo seznam/jailoc
```

To use your own libkrunfw build, point `lib_dir` in the [`[vm]` section](../reference/configuration.md) at a directory with your `libkrun` and `libkrunfw` libraries and set `runner` to a matching `jailoc-vm-runner` (or `go-microvm-runner`).

