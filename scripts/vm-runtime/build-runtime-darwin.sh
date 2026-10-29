#!/usr/bin/env bash
# Build the darwin/arm64 VM runtime bundle that jailoc downloads on first use
# of backend = "vm" (internal/vm/runtime.go).
#
# The bundle reuses libkrun and its GPU dependencies from a go-microvm release
# and replaces the runner with one built from the go-microvm revision jailoc
# pins in go.mod. The firmware tarball (libkrunfw, GPL-2.0) packages the
# Homebrew libkrunfw: go-microvm releases ship an older one, built without zram.
#
# Usage: build-runtime-darwin.sh GO_MICROVM_CHECKOUT UPSTREAM_TAG OUT_DIR
#   GO_MICROVM_CHECKOUT  go-microvm checkout at the revision pinned in go.mod
#   UPSTREAM_TAG         go-microvm release providing libkrun (e.g. v0.0.41)
#   OUT_DIR              receives go-microvm-runtime-darwin-arm64.tar.gz
#
# Requires: macOS on arm64, Go, Homebrew libkrun (to compile the runner) and
# libkrunfw, gh, go-task. Prints the SHA-256 values to pin in
# internal/vm/runtime.go.
set -euo pipefail

src=${1:?go-microvm checkout}
tag=${2:?upstream go-microvm release tag}
out=${3:?output directory}
name=go-microvm-runtime-darwin-arm64

[ "$(uname -s)/$(uname -m)" = "Darwin/arm64" ] || { echo "must run on macOS arm64" >&2; exit 1; }
mkdir -p "$out"
out=$(cd "$out" && pwd)
work=$(mktemp -d)
trap 'rm -r "$work"' EXIT

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
echo "$tag+$(git -C "$src" rev-parse --short HEAD)" > "$work/$name/VERSION"

if otool -L "$work/$name/go-microvm-runner" | grep -q /opt/homebrew; then
  echo "runner still links Homebrew libraries" >&2
  exit 1
fi

fw=go-microvm-firmware-darwin-arm64
mkdir "$work/$fw"
cp "$(brew --prefix libkrunfw)/lib/libkrunfw.5.dylib" "$work/$fw/"
chmod u+w "$work/$fw/libkrunfw.5.dylib"
install_name_tool -id @loader_path/libkrunfw.5.dylib "$work/$fw/libkrunfw.5.dylib"
codesign --force -s - "$work/$fw/libkrunfw.5.dylib"
brew list --versions libkrunfw | awk '{print "v" $2}' > "$work/$fw/VERSION"
cat > "$work/$fw/LICENSE-GPL" <<'EOF'
libkrunfw is licensed under the GNU General Public License v2.0 (GPL-2.0).
See https://github.com/libkrun/libkrunfw for full license text.
EOF

COPYFILE_DISABLE=1 tar -czf "$out/$name.tar.gz" -C "$work" "$name"
COPYFILE_DISABLE=1 tar -czf "$out/$fw.tar.gz" -C "$work" "$fw"
shasum -a 256 "$out/$name.tar.gz" "$out/$fw.tar.gz"
