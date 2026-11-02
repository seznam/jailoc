#!/usr/bin/env bash
# Collect the complete corresponding source of libkrunfw, which the VM runtime
# in jailoc releases contains: the Linux kernel inside it is GPL-2.0-only and
# the library is LGPL-2.1-only. Release workflows publish the result as
# jailoc_<version>_vm-runtime-source.tar next to the binaries.
#
# The libkrunfw source tree and the kernel tarball come from go-microvm's
# builder image, pinned by digest (builder-image.sh), the same image that
# builds the Linux libkrunfw (build-runtime-linux.sh). The macOS libkrunfw is built from the prebuilt
# kernel bundle of the same libkrunfw release (build-runtime-darwin.sh), which
# libkrunfw compiles from this source.
#
# Usage: build-runtime-source.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives vm-runtime-source.tar
#
# Requires: Linux, podman or docker.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
here=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT
runtime=$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)
# shellcheck source=scripts/vm-runtime/builder-image.sh
. "$here/builder-image.sh"
image=$(builder_image "$src")
dir=$work/vm-runtime-source
mkdir -p "$dir/go-microvm" "$dir/jailoc/scripts"

"$runtime" run --rm -v "$dir:/out" "$image" \
  sh -euc '
    tag=$(git -C /opt/libkrunfw describe --tags --exact-match)
    git -C /opt/libkrunfw archive --format=tar --prefix="libkrunfw-$tag/" \
      -o "/out/libkrunfw-$tag.tar" HEAD
    cp /opt/libkrunfw/tarballs/linux-*.tar.xz /out/
  '

cp "$src/LICENSE" "$src/Taskfile.yaml" "$src/versions.env" "$src/images/builder/Containerfile" "$dir/go-microvm/"
cp -R "$here" "$dir/jailoc/scripts/vm-runtime"
libkrunfw=$(cd "$dir" && ls libkrunfw-*.tar)
kernel=$(cd "$dir" && ls linux-*.tar.xz)

cat > "$dir/README" <<EOF
Corresponding source of libkrunfw in the jailoc VM runtime

$libkrunfw
    libkrunfw source tree: library code (LGPL-2.1-only), kernel patches and
    kernel configurations (GPL-2.0-only), and the Makefile that builds them.
$kernel
    Linux kernel source (GPL-2.0-only) that the libkrunfw Makefile downloads,
    patches, configures and builds.
go-microvm/
    go-microvm's builder image (Containerfile, versions.env, Taskfile.yaml)
    that builds libkrunfw for Linux.
jailoc/scripts/vm-runtime/
    jailoc's scripts that build the VM runtime. On macOS, libkrunfw is built
    from the prebuilt kernel bundle of the same libkrunfw release, which is
    generated from this source.

To rebuild libkrunfw, extract $libkrunfw, copy $kernel into its
tarballs/ directory and run make; see the libkrunfw README for the
requirements.

SHA-256:
$(cd "$dir" && sha256sum "$libkrunfw" "$kernel")
EOF

tar --owner=0 --group=0 -cf "$out/vm-runtime-source.tar" -C "$work" vm-runtime-source
sha256sum "$out/vm-runtime-source.tar"
