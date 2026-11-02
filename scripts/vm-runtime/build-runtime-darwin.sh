#!/usr/bin/env bash
# Build the darwin/arm64 VM runtime tarballs that release builds of jailoc
# embed (internal/vm/runtime.go).
#
# libkrun is built from the release go-microvm names in versions.env, with
# jailoc's patches (build-libkrun-linux.sh explains them; on arm64 they change
# nothing) and the same features as on Linux, so the runtime needs no GPU
# libraries. It is built with cargo-auditable, which records the Rust crates it
# contains so SBOM tools such as syft can list them. The runner is built from
# the go-microvm revision jailoc pins in go.mod against that libkrun. The
# firmware tarball builds libkrunfw from the prebuilt kernel bundle of the
# libkrunfw release go-microvm names in versions.env, pinned by checksum
# below. libkrunfw contains the Linux kernel (GPL-2.0-only) and is otherwise
# LGPL-2.1-only; its source is published with jailoc releases
# (build-runtime-source.sh).
#
# Usage: build-runtime-darwin.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives runtime.tar.gz and firmware.tar.gz, for
#                        example internal/vm/runtimebin/darwin-arm64
#
# Requires: macOS on arm64 with Xcode command line tools, Go, Rust (cargo),
# go-task, lld, xz, curl and git.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
here=$(cd "$(dirname "$0")" && pwd)
name=go-microvm-runtime-darwin-arm64
# libkrunfw-prebuilt-aarch64.tgz of this libkrunfw release; update both when
# go-microvm moves to a newer libkrunfw.
libkrunfw=v5.6.2
libkrunfw_sha256=adef6739cc3bda2a1b57f04c08ccbfdf206a6c831d76b32de1beddc0e7984e86
cargo_auditable=0.7.7

[ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ] || { echo "must run on macOS arm64" >&2; exit 1; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

version() { grep "^$1=" "$src/versions.env" | cut -d= -f2; }
libkrun=$(version LIBKRUN_VERSION)
if [ "$(version LIBKRUNFW_VERSION)" != "$libkrunfw" ]; then
  echo "go-microvm wants libkrunfw $(version LIBKRUNFW_VERSION), this script pins $libkrunfw" >&2
  exit 1
fi

git clone --quiet --depth 1 --branch "$libkrun" https://github.com/libkrun/libkrun "$work/libkrun"
for p in "$here"/libkrun-patches/*.patch; do
  echo "applying $(basename "$p")"
  patch -d "$work/libkrun" -p1 --forward < "$p"
done
# The Makefile runs cargo build; a cargo in front of the real one on PATH
# turns that into cargo auditable build.
cargo install --locked --root "$work/auditable" "cargo-auditable@$cargo_auditable"
printf '#!/bin/sh\nexec %s auditable "$@"\n' "$(command -v cargo)" > "$work/auditable/bin/cargo"
chmod +x "$work/auditable/bin/cargo"
# bindgen in libkrun's build scripts loads libclang at run time; use the one
# that ships with the active Xcode or command line tools.
if [ -z "${LIBCLANG_PATH:-}" ]; then
  dev=$(xcode-select -p)
  for dir in "$dev/Toolchains/XcodeDefault.xctoolchain/usr/lib" "$dev/usr/lib"; do
    if [ -e "$dir/libclang.dylib" ]; then
      export LIBCLANG_PATH=$dir
      break
    fi
  done
fi
PATH=$work/auditable/bin:$PATH make -C "$work/libkrun" BLK=1 NET=1
mkdir "$work/lib"
cp "$work"/libkrun/target/release/libkrun.1.*.dylib "$work/lib/libkrun.1.dylib"
otool -l "$work/lib/libkrun.1.dylib" | grep -q "sectname \.dep-v0" ||
  { echo "libkrun.1.dylib has no cargo-auditable data" >&2; exit 1; }
install_name_tool -id @loader_path/libkrun.1.dylib "$work/lib/libkrun.1.dylib"
ln -s libkrun.1.dylib "$work/lib/libkrun.dylib"

# The runner links the libkrun built above, found through CGO_LDFLAGS before
# the Homebrew directories go-microvm's cgo flags name.
(cd "$src" && CGO_LDFLAGS="-L$work/lib" task build-dev-darwin)
mkdir "$work/$name"
cp "$src/bin/go-microvm-runner" "$work/$name/go-microvm-runner"
cp "$work/lib/libkrun.1.dylib" "$work/$name/libkrun.1.dylib"
codesign --force -s - "$work/$name/libkrun.1.dylib"
if otool -L "$work/$name/go-microvm-runner" "$work/$name/libkrun.1.dylib" | grep -Eq '/opt/homebrew|/usr/local'; then
  echo "runtime links Homebrew libraries" >&2
  exit 1
fi
otool -L "$work/$name/go-microvm-runner" | grep -q '@loader_path/libkrun.1.dylib' ||
  { echo "runner does not load libkrun from its own directory" >&2; exit 1; }
rev=$(git -C "$src" rev-parse --short HEAD)
repo=$(git -C "$src" remote get-url origin)
install -m 0444 "$src/LICENSE" "$work/$name/LICENSE"
patches=$(cd "$here/libkrun-patches" && ls -- *.patch | cut -c1-4 | paste -sd, -)
echo "$rev libkrun-$libkrun+jailoc-patches-$patches" > "$work/$name/VERSION"
cat > "$work/$name/NOTICE" <<EOF
go-microvm-runner is built from go-microvm $rev ($repo) and
libkrun.1.dylib from libkrun $libkrun (https://github.com/libkrun/libkrun).
Both are licensed under the Apache License 2.0, see LICENSE.

jailoc modified libkrun with the patches $patches from
https://github.com/seznam/jailoc/tree/main/scripts/vm-runtime/libkrun-patches.

The notices of the Go modules and Rust crates built into these files are in
THIRD_PARTY_NOTICES in the jailoc release archive.
EOF

fw=go-microvm-firmware-darwin-arm64
mkdir "$work/$fw"
curl -fsSL -o "$work/libkrunfw.tgz" \
  "https://github.com/libkrun/libkrunfw/releases/download/$libkrunfw/libkrunfw-prebuilt-aarch64.tgz"
echo "$libkrunfw_sha256  $work/libkrunfw.tgz" | shasum -a 256 -c -
tar -xzf "$work/libkrunfw.tgz" -C "$work"
make -C "$work/libkrunfw"
kernel=$(sed -n 's/^KERNEL_VERSION = //p' "$work/libkrunfw/Makefile")
install -m 0644 "$work/libkrunfw/libkrunfw.5.dylib" "$work/$fw/libkrunfw.5.dylib"
install_name_tool -id @loader_path/libkrunfw.5.dylib "$work/$fw/libkrunfw.5.dylib"
codesign --force -s - "$work/$fw/libkrunfw.5.dylib"
install -m 0444 "$work/libkrunfw/LICENSE-GPL-2.0-only" "$work/libkrunfw/LICENSE-LGPL-2.1-only" "$work/$fw/"
echo "$libkrunfw" > "$work/$fw/VERSION"
cat > "$work/$fw/SOURCE" <<EOF
libkrunfw $libkrunfw (https://github.com/libkrun/libkrunfw) contains the Linux
kernel ($kernel), licensed under GPL-2.0-only (LICENSE-GPL-2.0-only).
The rest of libkrunfw is licensed under LGPL-2.1-only (LICENSE-LGPL-2.1-only).

The complete corresponding source code, including the Linux kernel source,
the libkrunfw patches and kernel configuration, and the build scripts, is
published as jailoc_<version>_vm-runtime-source.tar with every jailoc release
at https://github.com/seznam/jailoc/releases, next to the binaries.
EOF

COPYFILE_DISABLE=1 tar -czf "$out/runtime.tar.gz" -C "$work" "$name"
COPYFILE_DISABLE=1 tar -czf "$out/firmware.tar.gz" -C "$work" "$fw"
shasum -a 256 "$out/runtime.tar.gz" "$out/firmware.tar.gz"
