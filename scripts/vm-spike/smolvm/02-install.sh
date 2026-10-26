#!/bin/bash
set -u
cd "$(dirname "$0")/../../.." || exit 1
BASE="$PWD/scripts/vm-spike/smolvm"
HOME_DIR="$PWD/.spike-state/s"
mkdir -p "$BASE/logs" "$HOME_DIR/.smolvm" "$HOME_DIR/Library/Application Support/smolvm" "$BASE/results"
LOG="$BASE/logs/02.log"
(
set -ex
date -u
df -h /
curl -fLsS --retry 2 'https://github.com/smol-machines/smolvm/releases/download/v1.32.3/checksums.sha256' -o "$BASE/results/checksums.sha256"
curl -fLsS --retry 2 'https://github.com/smol-machines/smolvm/releases/download/v1.32.3/smolvm-1.32.3-darwin-arm64.tar.gz' -o "$BASE/results/smolvm-1.32.3-darwin-arm64.tar.gz"
grep 'smolvm-1.32.3-darwin-arm64.tar.gz' "$BASE/results/checksums.sha256" | ( cd "$BASE/results" && shasum -a 256 -c )
tar -tzf "$BASE/results/smolvm-1.32.3-darwin-arm64.tar.gz" | rg '^smolvm-1.32.3-darwin-arm64/(smolvm|smolvm-bin|lib/|agent-rootfs/|storage-template|overlay-template)' | wc -l
tar -xzf "$BASE/results/smolvm-1.32.3-darwin-arm64.tar.gz" -C "$HOME_DIR/.smolvm" --strip-components=1
ls -la "$HOME_DIR/.smolvm"
ls -la "$HOME_DIR/.smolvm/agent-rootfs"
cp -R "$HOME_DIR/.smolvm/agent-rootfs" "$HOME_DIR/Library/Application Support/smolvm/"
codesign -dv --verbose=2 "$HOME_DIR/.smolvm/smolvm-bin" || true
export HOME="$HOME_DIR"
"$HOME_DIR/.smolvm/smolvm" --version
"$HOME_DIR/.smolvm/smolvm" machine create --help
) >"$LOG" 2>&1
status=$?
printf '02 install exit=%s\n' "$status"
rg 'OK|smolvm [0-9]|Identifier=|Authority=|Error|error:|Avail' "$LOG" | tail -12
printf 'Evidence: %s\n' "$LOG"
exit "$status"
