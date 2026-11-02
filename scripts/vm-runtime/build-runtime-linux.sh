#!/usr/bin/env bash
# Build the linux VM runtime tarballs for the host architecture (amd64 or
# arm64) that release builds of jailoc embed (internal/vm/runtime.go).
#
# go-microvm's builder image compiles the runner from the go-microvm revision
# jailoc pins in go.mod and provides libkrun and libkrunfw at the versions
# that revision names in versions.env, so the runner and the libraries come
# from the same glibc and match the combination the VM integration test uses.
# libkrun is then rebuilt with jailoc's patches (build-libkrun-linux.sh).
# The firmware tarball carries libkrunfw (GPL-2.0) separately.
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

(cd "$src" && task build-runner)
"$(dirname "$0")/build-libkrun-linux.sh" "$src" "$src/bin"
libkrun=$(grep '^LIBKRUN_VERSION=' "$src/versions.env" | cut -d= -f2)
libkrunfw=$(grep '^LIBKRUNFW_VERSION=' "$src/versions.env" | cut -d= -f2)
rev=$(git -C "$src" rev-parse --short HEAD)

mkdir -p "$work/$runtime_name" "$work/$firmware_name"
install -m 0755 "$src/bin/go-microvm-runner" "$work/$runtime_name/go-microvm-runner"
install -m 0444 "$src/bin/libkrun.so.1" "$work/$runtime_name/libkrun.so.1"
patches=$(cd "$(dirname "$0")/libkrun-patches" && ls -- *.patch | cut -c1-4 | paste -sd, -)
echo "$rev libkrun-$libkrun+jailoc-patches-$patches" > "$work/$runtime_name/VERSION"
install -m 0444 "$src/bin/libkrunfw.so.5" "$work/$firmware_name/libkrunfw.so.5"
echo "$libkrunfw" > "$work/$firmware_name/VERSION"
cat > "$work/$firmware_name/LICENSE-GPL" <<'EOF'
libkrunfw is licensed under the GNU General Public License v2.0 (GPL-2.0).
See https://github.com/libkrun/libkrunfw for full license text.
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
