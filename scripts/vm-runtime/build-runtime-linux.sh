#!/usr/bin/env bash
# Build the linux VM runtime tarballs for the host architecture (amd64 or
# arm64) that release builds of jailoc embed (internal/vm/runtime.go).
#
# go-microvm's builder image compiles the runner from the go-microvm revision
# jailoc pins in go.mod and provides libkrun and libkrunfw at the versions
# that revision names in versions.env, so the runner and the libraries come
# from the same glibc and match the combination the VM integration test uses.
# The builder image is pinned by digest (builder-image.sh).
# libkrun is then rebuilt with jailoc's patches (build-libkrun-linux.sh).
# The firmware tarball carries libkrunfw (GPL-2.0-only kernel, LGPL-2.1-only
# library) with its license texts and where to get its source.
#
# Usage: build-runtime-linux.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives runtime.tar.gz and firmware.tar.gz, for
#                        example internal/vm/runtimebin/linux-amd64
#
# Requires: Linux on x86_64 or aarch64, go-task, podman or docker.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
case "$(uname -s)/$(uname -m)" in
  Linux/x86_64) arch=amd64 ;;
  Linux/aarch64) arch=arm64 ;;
  *) echo "must run on Linux x86_64 or aarch64" >&2; exit 1 ;;
esac
runtime_name=go-microvm-runtime-linux-$arch
firmware_name=go-microvm-firmware-linux-$arch
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
here=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=scripts/vm-runtime/builder-image.sh
. "$here/builder-image.sh"
builder_image "$src" >/dev/null
runtime=$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)

# go-microvm's Taskfile refers to the builder image by tag; point the tag at
# the pinned digest so the runner is built in the same image.
"$runtime" pull "$builder_repo@$builder_digest"
"$runtime" tag "$builder_repo@$builder_digest" "$builder_repo:$builder_tag"
(cd "$src" && task build-runner)
"$here/build-libkrun-linux.sh" "$src" "$src/bin"
version() { grep "^$1=" "$2" | cut -d= -f2; }
libkrun=$(version LIBKRUN_VERSION "$src/bin/builder-versions.env")
libkrunfw=$(version LIBKRUNFW_VERSION "$src/bin/builder-versions.env")
kernel=$(version KERNEL_VERSION "$src/bin/builder-versions.env")
if [ "$libkrun" != "$(version LIBKRUN_VERSION "$src/versions.env")" ] ||
  [ "$libkrunfw" != "$(version LIBKRUNFW_VERSION "$src/versions.env")" ]; then
  echo "builder image has libkrun $libkrun and libkrunfw $libkrunfw, not the versions in $src/versions.env" >&2
  exit 1
fi
rev=$(git -C "$src" rev-parse --short HEAD)
repo=$(git -C "$src" remote get-url origin)

mkdir -p "$work/$runtime_name" "$work/$firmware_name"
install -m 0755 "$src/bin/go-microvm-runner" "$work/$runtime_name/go-microvm-runner"
install -m 0444 "$src/bin/libkrun.so.1" "$work/$runtime_name/libkrun.so.1"
install -m 0444 "$src/LICENSE" "$work/$runtime_name/LICENSE"
patches=$(cd "$(dirname "$0")/libkrun-patches" && ls -- *.patch | cut -c1-4 | paste -sd, -)
echo "$rev libkrun-$libkrun+jailoc-patches-$patches" > "$work/$runtime_name/VERSION"
cat > "$work/$runtime_name/NOTICE" <<EOF
go-microvm-runner is built from go-microvm $rev ($repo) and
libkrun.so.1 from libkrun $libkrun (https://github.com/libkrun/libkrun).
Both are licensed under the Apache License 2.0, see LICENSE.

jailoc modified libkrun with the patches $patches from
https://github.com/seznam/jailoc/tree/main/scripts/vm-runtime/libkrun-patches.

The notices of the Go modules and Rust crates built into these files are in
THIRD_PARTY_NOTICES in the jailoc release archive.
EOF
install -m 0444 "$src/bin/libkrunfw.so.5" "$work/$firmware_name/libkrunfw.so.5"
install -m 0444 "$src/bin/LICENSE-GPL-2.0-only" "$src/bin/LICENSE-LGPL-2.1-only" "$work/$firmware_name/"
echo "$libkrunfw" > "$work/$firmware_name/VERSION"
cat > "$work/$firmware_name/SOURCE" <<EOF
libkrunfw $libkrunfw (https://github.com/libkrun/libkrunfw) contains the Linux
kernel ($kernel), licensed under GPL-2.0-only (LICENSE-GPL-2.0-only).
The rest of libkrunfw is licensed under LGPL-2.1-only (LICENSE-LGPL-2.1-only).

The complete corresponding source code, including the Linux kernel source,
the libkrunfw patches and kernel configuration, and the build scripts, is
published as jailoc_<version>_vm-runtime-source.tar with every jailoc release
at https://github.com/seznam/jailoc/releases, next to the binaries.
EOF

# The runner finds libkrun through LD_LIBRARY_PATH, which jailoc sets to the
# unpacked runtime and firmware directories; it must not need anything else
# outside the system libraries.
missing=$(LD_LIBRARY_PATH="$work/$runtime_name:$work/$firmware_name" ldd "$work/$runtime_name/go-microvm-runner" | grep 'not found' || true)
if [ -n "$missing" ]; then
  echo "runner has unresolved libraries:" >&2
  echo "$missing" >&2
  exit 1
fi

tar --owner=0 --group=0 -czf "$out/runtime.tar.gz" -C "$work" "$runtime_name"
tar --owner=0 --group=0 -czf "$out/firmware.tar.gz" -C "$work" "$firmware_name"
sha256sum "$out/runtime.tar.gz" "$out/firmware.tar.gz"
