# How to install jailoc

jailoc runs on macOS on Apple Silicon and on Linux amd64 (arm64 experimental) with KVM and Landlock. [Check your platform](#check-your-platform) first, then install with Homebrew, a pre-built binary, [Nix](#install-with-nix) or [from source](#build-from-source).

<a class="md-button md-button--primary" href="#install-with-homebrew">Install with Homebrew</a>
<a class="md-button" href="#install-a-pre-built-binary">Choose a pre-built binary</a>

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
    On Linux, libkrun's virtio-fs shows the real host owner of every file, so `jailoc-guest` mounts the boot root (jailoc's own files) and the shares idmapped: inside the VM your files appear as root's or the agent's, and on the host everything belongs to your user. The image layers keep the image's own owners. Root inside the VM cannot give a shared file another host owner. If the guest kernel rejects idmapped mounts, the agent falls back to your user's UID and GID, and `jailoc-guest` idmaps the image layers so that the agent's files in the image appear as yours.

Other platforms, including Intel Macs, are unsupported. If you build the [go-microvm](https://github.com/stacklok/go-microvm) runner and libkrun yourself, point jailoc at them in the [`[vm]` section](../reference/configuration.md):

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/libkrun/lib"
```

## Know when Docker is needed

The workspace VMs don't use Docker. You need Docker Engine on the host only for:

- building workspace images when `[vm] builder` is `"docker"`, or `"auto"` (the default) with a usable daemon (see [How to use a custom image](custom-images.md) and the [`builder` field](../reference/configuration.md));
- copying existing Docker-volume data with [`jailoc migrate`](migrate-from-v1.md).

Without a usable host daemon, `"auto"` builds images in a builder VM. A workspace with `isolated_build = true` always builds in its own builder VM.

## Install with Homebrew

!!! note "Unsigned binary"
    jailoc binaries are not signed or notarized by Apple. The cask removes the `com.apple.quarantine` attribute from `jailoc` after installation so that Gatekeeper does not block it.

On macOS and Linux, install the latest release from the [seznam/homebrew-tap](https://github.com/seznam/homebrew-tap) tap:

```bash
brew install --cask seznam/tap/jailoc
```

Upgrade with `brew upgrade --cask jailoc`. The tap carries stable releases only; install release candidates from the [GitHub Releases page](https://github.com/seznam/jailoc/releases). The cask installs both `jailoc` and the `jl` alias on your `PATH`.

## Install with Nix

Run jailoc without installing it:

```bash
nix run github:seznam/jailoc
```

To install it, add the flake to your inputs:

```nix
{
  inputs.jailoc.url = "github:seznam/jailoc";
}
```

Then include the package in `environment.systemPackages` or `home.packages`:

```nix
inputs.jailoc.packages.${pkgs.stdenv.hostPlatform.system}.jailoc
```

On Linux (x86_64, aarch64), the package builds the VM runtime (go-microvm runner, patched libkrun, libkrunfw) from source and points jailoc at it, so you don't need a `[vm]` section. A `runner` set in the [`[vm]` section](../reference/configuration.md) takes precedence. Hosts without access to a binary cache compile these components on the first build, including a Linux kernel for libkrunfw, which takes a while.

!!! note "aarch64-darwin"
    On macOS the Nix package has no VM runtime. Set `runner` and `lib_dir` in the [`[vm]` section](../reference/configuration.md) as shown in [Check your platform](#check-your-platform), or install with [Homebrew](#install-with-homebrew) instead.

## Install a pre-built binary

Every release publishes archives for macOS (arm64) and Linux (amd64, arm64). Each contains `jailoc`, the `jl` alias, `LICENSE`, `README.md` and `THIRD_PARTY_NOTICES`. Download the archive for your platform:

{% set version = version or ('v' ~ release['.']) %}

<a class="md-button md-button--primary" href="https://github.com/seznam/jailoc/releases/download/{{ version }}/jailoc_{{ version[1:] }}_darwin_arm64.tar.gz">Download {{ version }} for macOS on Apple Silicon</a>
<a class="md-button" href="https://github.com/seznam/jailoc/releases/download/{{ version }}/jailoc_{{ version[1:] }}_linux_amd64.tar.gz">Download {{ version }} for Linux amd64</a>
<a class="md-button" href="https://github.com/seznam/jailoc/releases/download/{{ version }}/jailoc_{{ version[1:] }}_linux_arm64.tar.gz">Download {{ version }} for Linux arm64 (experimental)</a>

These archives are for {{ version }}; verify them against its [SHA-256 checksums](https://github.com/seznam/jailoc/releases/download/{{ version }}/checksums.txt). Other releases are on GitHub:

<a class="md-button" href="https://github.com/seznam/jailoc/releases/latest">Open the latest release on GitHub</a>

Extract the archive and place `jailoc` (and `jl`, if you want the alias) on your `PATH`.

!!! note "Gatekeeper on macOS"
    The binary isn't signed or notarized by Apple, so Gatekeeper can block a `jailoc` downloaded with a browser. Remove the quarantine attribute, as the Homebrew cask does: `xattr -d com.apple.quarantine jailoc`.

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

Release binaries embed the VM runtime for their platform. The first `jailoc up` unpacks it into `~/.cache/jailoc-vm/runtime/` and later starts reuse it; nothing is downloaded. On Linux, the [Nix package](#install-with-nix) references its runtime in the Nix store instead.

To use your own libkrunfw build, point `lib_dir` in the [`[vm]` section](../reference/configuration.md) at a directory with your `libkrun` and `libkrunfw` libraries and set `runner` to a matching `jailoc-vm-runner` (or `go-microvm-runner`).

### Check the runtime licenses

jailoc itself is MIT-licensed. The embedded runtime, and the Nix-built components, carry their own licenses:

| Component | License | Notes |
|-----------|---------|-------|
| `jailoc-vm-runner` | Apache-2.0 | Built from [go-microvm](https://github.com/stacklok/go-microvm); runs as a separate process |
| libkrun | Apache-2.0 | Built with the patches in `scripts/vm-runtime/libkrun-patches/` |
| musl libc | MIT | Statically linked into libkrun's guest init |
| libkrunfw | LGPL-2.1-only (library), GPL-2.0-only (bundled Linux kernel) | |

Each release provides these compliance artifacts:

- **Notices:** every release archive contains `THIRD_PARTY_NOTICES` with the license texts of all bundled Go modules, Rust crates and runtime libraries. Each unpacked runtime directory contains `LICENSE`, `NOTICE`, the libkrunfw license texts and a `SOURCE` file.
- **Corresponding source:** every [GitHub Release](https://github.com/seznam/jailoc/releases) attaches the complete corresponding source of libkrunfw, including the Linux kernel tarball and build scripts, as `jailoc_<version>_vm-runtime-source.tar`.
- **SBOMs:** every release has SPDX SBOMs, generated by [syft](https://github.com/anchore/syft). `<archive>.sbom.json` lists the Go modules in jailoc, and `jailoc_<version>_vm-runtime_<os>_<arch>.sbom.json` lists the Go modules in the embedded `jailoc-vm-runner` and the Rust crates in libkrun (built with [cargo-auditable](https://github.com/rust-secure-code/cargo-auditable)). syft can't detect libkrunfw, so `THIRD_PARTY_NOTICES` remains the complete list.
- **Attestations:** release assets carry [build provenance attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations). Check that the jailoc release workflow built a file:

```bash
gh attestation verify jailoc_<version>_linux_amd64.tar.gz --repo seznam/jailoc
```

- **Immutable releases:** a release is published only after every asset is attested, and its assets and tag can't change afterwards. Check that a downloaded file matches the published release:

```bash
gh release verify-asset v<version> jailoc_<version>_linux_amd64.tar.gz --repo seznam/jailoc
```

