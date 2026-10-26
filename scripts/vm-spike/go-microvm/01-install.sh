#!/bin/bash
set -uo pipefail
BASE="$(cd "$(dirname "$0")" && pwd)"
mkdir -p "$BASE/logs" "$BASE/spike"
exec > >(tee "$BASE/logs/01.log") 2>&1
set -x
ROOT="$(cd "$BASE/../../.." && pwd)"
date -u
df -h /
go version
git -C "$BASE/upstream" rev-parse HEAD
brew list --versions libkrun libkrunfw || true
if ! test -f /opt/homebrew/lib/libkrun.1.dylib || ! test -f /opt/homebrew/lib/libkrunfw.5.dylib; then
  for attempt in $(seq 1 10); do
    brew tap slp/krun && brew trust slp/krun && brew install libkrun libkrunfw && break
    if test "$attempt" -eq 10; then break; fi
    sleep 30
  done
fi
brew list --versions libkrun libkrunfw || true
if test -f /opt/homebrew/lib/libkrun.1.dylib && test -f /opt/homebrew/lib/libkrunfw.5.dylib; then
  (cd "$BASE/upstream" && CGO_ENABLED=1 go build -o "$ROOT/.spike-state/go-microvm-runner" ./runner/cmd/go-microvm-runner)
  codesign --entitlements "$BASE/upstream/assets/entitlements.plist" --force -s - "$ROOT/.spike-state/go-microvm-runner"
  codesign -d --entitlements - "$ROOT/.spike-state/go-microvm-runner"
  (cd "$BASE/spike" && go mod tidy && go build -o "$ROOT/.spike-state/spike" .)
fi
date -u
df -h /
printf 'Step 1: libkrun=%s libkrunfw=%s runner=%s spike=%s\n' "$(test -f /opt/homebrew/lib/libkrun.1.dylib && printf yes || printf no)" "$(test -f /opt/homebrew/lib/libkrunfw.5.dylib && printf yes || printf no)" "$(test -x "$ROOT/.spike-state/go-microvm-runner" && printf yes || printf no)" "$(test -x "$ROOT/.spike-state/spike" && printf yes || printf no)"
printf 'Step 1: libkrun install/build evidence %s\n' "$BASE/logs/01.log"
printf '1. Install: see logs/01.log; runner and library build status at end of log.\n' >> "$BASE/PROGRESS.md"
