#!/usr/bin/env bash
# Rebuild libkrun for Linux with the patches in libkrun-patches/.
#
# go-microvm's builder image ships libkrun at the release go-microvm pins.
# jailoc needs fixes that are merged upstream but not released yet:
#   0001  raise the x86_64 virtio IRQ range from 5-15 to 5-23 (libkrun
#         f5fc9f25). With 11 lines a workspace VM runs out of interrupts
#         after six shared directories; with 19 it fits typical workspaces.
#   0002  describe IOAPIC pins 16-23 in the MP table so the guest can route
#         the devices on them (not upstream: libkrun describes them only
#         through its opt-in ACPI tables).
# Drop 0001 once go-microvm pins a libkrun release that contains it. Both
# patches touch only x86_64 code; on aarch64 they apply and change nothing.
# The patches are applied to the source tree inside the builder image,
# which has the matching toolchain, and the result replaces libkrun.so.1.
#
# The image tag names only the libkrun release, so the versions of libkrun
# and libkrunfw are read from their source trees in the image, and libkrunfw
# with its license texts is copied from the same image. The image is pinned by
# digest (builder-image.sh).
#
# libkrun is built with cargo-auditable, which records the Rust crates it
# contains in a .dep-v0 section, so SBOM tools such as syft can list them.
#
# Usage: build-libkrun-linux.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives libkrun.so.1, libkrunfw.so.5, the libkrunfw
#                        LICENSE-* files and builder-versions.env
#
# Requires: Linux on x86_64 or aarch64, podman or docker.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
here=$(cd "$(dirname "$0")" && pwd)
patches=$here/libkrun-patches
# shellcheck source=scripts/vm-runtime/builder-image.sh
. "$here/builder-image.sh"

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64 | Linux/aarch64) ;;
  *) echo "must run on Linux x86_64 or aarch64" >&2; exit 1 ;;
esac
mkdir -p "$out"
out=$(cd "$out" && pwd)
runtime=$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)
image=$(builder_image "$src")
cargo_auditable=0.7.7

"$runtime" run --rm \
  -v "$patches:/patches:ro" -v "$out:/out" \
  -e CARGO_AUDITABLE_VERSION="$cargo_auditable" \
  "$image" \
  sh -euc '
    libkrun=$(git -C /opt/libkrun describe --tags --exact-match)
    libkrunfw=$(git -C /opt/libkrunfw describe --tags --exact-match)
    kernel=$(sed -n "s/^KERNEL_VERSION = //p" /opt/libkrunfw/Makefile)
    printf "LIBKRUN_VERSION=%s\nLIBKRUNFW_VERSION=%s\nKERNEL_VERSION=%s\n" \
      "$libkrun" "$libkrunfw" "$kernel" > /out/builder-versions.env
    cp /opt/libkrun-built/libkrunfw.so.5 /opt/libkrunfw/LICENSE-GPL-2.0-only \
      /opt/libkrunfw/LICENSE-LGPL-2.1-only /out/
    cd /opt/libkrun
    for p in /patches/*.patch; do
      echo "applying $(basename "$p")"
      patch -p1 --forward < "$p"
    done
    # The Makefile runs cargo build; a cargo in front of the real one on PATH
    # turns that into cargo auditable build.
    cargo install --locked --root /opt/auditable "cargo-auditable@$CARGO_AUDITABLE_VERSION"
    printf "#!/bin/sh\nexec %s auditable \"\$@\"\n" "$(command -v cargo)" > /opt/auditable/bin/cargo
    chmod +x /opt/auditable/bin/cargo
    PATH=/opt/auditable/bin:$PATH make BLK=1 NET=1
    cp target/release/libkrun.so.1.* /out/libkrun.so.1
    readelf -S /out/libkrun.so.1 | grep -q "\.dep-v0" ||
      { echo "libkrun.so.1 has no cargo-auditable data" >&2; exit 1; }
  '
echo "patched libkrun: $out/libkrun.so.1"
