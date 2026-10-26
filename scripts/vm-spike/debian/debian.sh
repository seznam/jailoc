#!/usr/bin/env bash
# Debian + systemd as PID 1 in a go-microvm workspace behind jailoc-netbroker.
# Stage 0 pulls debian:trixie into the image cache, stage 1 installs systemd and
# Docker into a persistent copy of that rootfs, stage 2 boots it twice with
# systemd as init and records what jlp-verify.service sees.
#
# Required: RUNNER (go-microvm-runner with virtio-blk disk support, see
# stacklok/go-microvm#128, that passes GO_MICROVM_INIT_ENV to libkrun's init
# so KRUN_INIT_PID1=1 makes systemd PID 1), CACHE (OCI image cache dir that also holds an
# Alpine image, whose busybox configures the network in stage 1).
# Optional: STATE (default $HOME/.cache/jailoc-vmspike/debian).
set -uo pipefail
cd "$(dirname "$0")/../../.."
RUNNER=${RUNNER:?set RUNNER}
CACHE=${CACHE:?set CACHE}
S=${STATE:-$HOME/.cache/jailoc-vmspike/debian}
D=scripts/vm-spike/debian
NET=192.168.129
mkdir -p "$S/bin"
exec > >(tee "$S/debian.log") 2>&1
set -x
date -u

go build -o "$S/bin/vmprobe" ./cmd/jailoc-vmprobe || exit 1
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$S/bin/vmprobe-linux" ./cmd/jailoc-vmprobe || exit 1
go build -o "$S/bin/jailoc-netbroker" ./cmd/jailoc-netbroker || exit 1
(cd scripts/vm-spike/multi/launcher && CGO_ENABLED=1 go build -o "$S/bin/launcher" .) || exit 1

for d in shares state rootfs; do [ -d "$S/$d" ] && rm -r "$S/$d"; done
mkdir -p "$S/shares/jlp/out" "$S/state"
echo '{}' > "$S/policy.json"
echo $NET > "$S/shares/jlp/net"
"$S/bin/vmprobe" plan -candidate go-microvm+netbroker+debian-systemd -dir "$S/shares/fx" -guest-prefix /mnt/fx \
  -port 47851 -alt-port 47852 -ipv6 disabled -export "$S/shares/jlp:/mnt/jlp:rw" -out "$S/shares/jlp/plan.json" || exit 1
cp "$S/bin/vmprobe-linux" "$D/verify.sh" "$S/shares/jlp/"
musl=$(find "$CACHE" -maxdepth 3 -path '*/lib/ld-musl-aarch64.so.1' | head -1)
cp "$musl" "$(dirname "$(dirname "$musl")")/bin/busybox" "$S/shares/jlp/" || exit 1

launch() { # label timeout args...
  local label=$1 to=$2; shift 2
  local t0=$(date +%s)
  timeout "$to" "$S/bin/launcher" -name deb -mem 2048 -cpus 2 -state "$S/state" -cache "$CACHE" \
    -shares "$S/shares" -runner "$RUNNER" -broker "$S/bin/jailoc-netbroker" -policy "$S/policy.json" \
    -subnet "$NET.0/24" "$@" > "$S/launcher-$label.log" 2>&1
  echo "$label launcher rc=$? after $(( $(date +%s) - t0 ))s"
  grep -E 'broker-|vm-|nic-|supervise-' "$S/launcher-$label.log"
}

# Stage 0: pull debian:trixie (the init exits at once).
launch stage0 300 -image debian:trixie -init-cmd /bin/true
src=$(grep -l . "$CACHE"/sha256-*/etc/debian_version 2>/dev/null | xargs ls -t | head -1)
[ -n "$src" ] || { echo "debian rootfs not found in cache"; exit 1; }
ditto "$(dirname "$(dirname "$src")")" "$S/rootfs" || exit 1

# Stage 1: install systemd and Docker into the persistent rootfs.
launch stage1 1200 -rootfs "$S/rootfs" -init "$D/stage1.sh"
[ -e "$S/shares/jlp/out/STAGE1-DONE" ] || { echo "stage 1 failed"; tail -30 "$S/shares/jlp/out/stage1.log"; exit 1; }
grep 'apt seconds' "$S/shares/jlp/out/stage1.log"
echo "data disk bytes after stage 1: $(stat -f %z "$S/state/docker.img") (created with $((10<<30)))"

# Stage 2: systemd as PID 1, twice (second boot checks persistence).
export GO_MICROVM_INIT_ENV=KRUN_INIT_PID1=1
for b in 1 2; do
  echo $b > "$S/shares/jlp/boot"
  launch boot$b 900 -rootfs "$S/rootfs" -init-cmd /lib/systemd/systemd
  [ -e "$S/shares/jlp/out/VERIFIED-$b" ] && echo "boot $b verified" || echo "boot $b NOT verified"
  echo "data disk bytes after boot $b: $(stat -f %z "$S/state/docker.img")"
done
pgrep -fl 'jailoc-netbroker|go-microvm-runner' || echo "no broker or runner left"
echo DEBIAN-DONE
