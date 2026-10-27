#!/usr/bin/env bash
# Build the linux/amd64 VM runtime bundles that jailoc downloads on first use
# of the VM backend (internal/vm/runtime.go).
#
# go-microvm's builder image compiles the runner from the go-microvm revision
# jailoc pins in go.mod and provides libkrun and libkrunfw at the versions
# that revision names in versions.env, so the runner and the libraries come
# from the same glibc and match the combination the VM integration test uses.
# The firmware tarball carries libkrunfw (GPL-2.0) separately.
#
# Usage: build-runtime-linux.sh GO_MICROVM_CHECKOUT OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   OUT_DIR              receives go-microvm-runtime-linux-amd64.tar.gz and
#                        go-microvm-firmware-linux-amd64.tar.gz
#
# Requires: Linux on x86_64, go-task, podman or docker. Prints the SHA-256
# values to pin in internal/vm/runtime.go.
set -euo pipefail

src=${1:?go-microvm checkout}
out=${2:?output directory}
runtime_name=go-microvm-runtime-linux-amd64
firmware_name=go-microvm-firmware-linux-amd64

[ "$(uname -s)/$(uname -m)" = "Linux/x86_64" ] || { echo "must run on Linux x86_64" >&2; exit 1; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

(cd "$src" && task build-runner)
libkrun=$(grep '^LIBKRUN_VERSION=' "$src/versions.env" | cut -d= -f2)
libkrunfw=$(grep '^LIBKRUNFW_VERSION=' "$src/versions.env" | cut -d= -f2)
rev=$(git -C "$src" rev-parse --short HEAD)

mkdir -p "$work/$runtime_name" "$work/$firmware_name"
install -m 0755 "$src/bin/go-microvm-runner" "$work/$runtime_name/go-microvm-runner"
install -m 0444 "$src/bin/libkrun.so.1" "$work/$runtime_name/libkrun.so.1"
echo "$rev libkrun-$libkrun" > "$work/$runtime_name/VERSION"
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

tar --owner=0 --group=0 -czf "$out/$runtime_name.tar.gz" -C "$work" "$runtime_name"
tar --owner=0 --group=0 -czf "$out/$firmware_name.tar.gz" -C "$work" "$firmware_name"
sha256sum "$out/$runtime_name.tar.gz" "$out/$firmware_name.tar.gz"
