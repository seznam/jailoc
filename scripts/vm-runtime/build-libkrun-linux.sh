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
# Usage: build-libkrun-linux.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives libkrun.so.1
#
# Requires: Linux on x86_64 or aarch64, podman or docker.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
patches=$(cd "$(dirname "$0")/libkrun-patches" && pwd)

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64 | Linux/aarch64) ;;
  *) echo "must run on Linux x86_64 or aarch64" >&2; exit 1 ;;
esac
mkdir -p "$out"
out=$(cd "$out" && pwd)
runtime=$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)
version=$(grep '^LIBKRUN_VERSION=' "$src/versions.env" | cut -d= -f2)

"$runtime" run --rm \
  -v "$patches:/patches:ro" -v "$out:/out" \
  "ghcr.io/stacklok/go-microvm-builder:$version" \
  sh -euc '
    cd /opt/libkrun
    for p in /patches/*.patch; do
      echo "applying $(basename "$p")"
      patch -p1 --forward < "$p"
    done
    make BLK=1 NET=1
    cp target/release/libkrun.so.1.* /out/libkrun.so.1
  '
echo "patched libkrun $version: $out/libkrun.so.1"
