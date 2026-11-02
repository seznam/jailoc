#!/usr/bin/env bash
# Build the darwin/arm64 VM runtime tarballs that release builds of jailoc
# embed (internal/vm/runtime.go).
#
# The bundle reuses libkrun and its GPU dependencies from a go-microvm release
# and replaces the runner with one built from the go-microvm revision jailoc
# pins in go.mod. The firmware tarball builds libkrunfw from the prebuilt
# kernel bundle of the libkrunfw release go-microvm names in versions.env
# (go-microvm releases ship an older one, built without zram), pinned by
# checksum below. libkrunfw contains the Linux kernel (GPL-2.0-only) and is
# otherwise LGPL-2.1-only; its source is published with jailoc releases
# (build-runtime-source.sh).
#
# Usage: build-runtime-darwin.sh GO_MICROVM_CHECKOUT UPSTREAM_TAG OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   UPSTREAM_TAG         go-microvm release providing libkrun (e.g. v0.0.41)
#   OUT_DIR              receives runtime.tar.gz and firmware.tar.gz, for
#                        example internal/vm/runtimebin/darwin-arm64
#
# Requires: macOS on arm64, Go, Homebrew libkrun (to compile the runner), gh,
# go-task, curl, make and a C compiler.
set -euo pipefail

src=${1:?go-microvm checkout}
tag=${2:?upstream go-microvm release tag}
out=${3:?output directory}
name=go-microvm-runtime-darwin-arm64
# libkrunfw-prebuilt-aarch64.tgz of this libkrunfw release; update both when
# go-microvm moves to a newer libkrunfw.
libkrunfw=v5.6.2
libkrunfw_sha256=adef6739cc3bda2a1b57f04c08ccbfdf206a6c831d76b32de1beddc0e7984e86

[ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ] || { echo "must run on macOS arm64" >&2; exit 1; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

want=$(grep '^LIBKRUNFW_VERSION=' "$src/versions.env" | cut -d= -f2)
if [ "$want" != "$libkrunfw" ]; then
  echo "go-microvm wants libkrunfw $want, this script pins $libkrunfw" >&2
  exit 1
fi

gh release download "$tag" --repo stacklok/go-microvm \
  --pattern "$name.tar.gz" --pattern sha256sums.txt \
  --dir "$work"
(cd "$work" && grep -- "$name.tar.gz" sha256sums.txt | shasum -a 256 -c -)
tar -xzf "$work/$name.tar.gz" -C "$work"

(cd "$src" && task build-dev-darwin)
cp "$src/bin/go-microvm-runner" "$work/$name/go-microvm-runner"
install_name_tool -change "$(brew --prefix libkrun)/lib/libkrun.1.dylib" \
  @loader_path/libkrun.1.dylib "$work/$name/go-microvm-runner"
codesign --force -s - --entitlements "$src/assets/entitlements.plist" "$work/$name/go-microvm-runner"
rev=$(git -C "$src" rev-parse --short HEAD)
echo "$tag+$rev" > "$work/$name/VERSION"
install -m 0444 "$src/LICENSE" "$work/$name/LICENSE"
cat > "$work/$name/NOTICE" <<EOF
go-microvm-runner is built from go-microvm $rev ($(git -C "$src" remote get-url origin)),
licensed under the Apache License 2.0, see LICENSE. libkrun (Apache-2.0),
virglrenderer (MIT), libepoxy (MIT) and MoltenVK (Apache-2.0) come unmodified
from the go-microvm $tag release (https://github.com/stacklok/go-microvm).

The license texts and notices of these libraries and of the Go modules and
Rust crates built into these files are in THIRD_PARTY_NOTICES in the jailoc
release archive.
EOF

if otool -L "$work/$name/go-microvm-runner" | grep -q /opt/homebrew; then
  echo "runner still links Homebrew libraries" >&2
  exit 1
fi

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
