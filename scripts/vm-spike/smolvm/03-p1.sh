#!/usr/bin/env bash
# P1 steps 2-8 for smolvm: create/boot, guest probes, in-guest image build, privileged
# Testcontainers + dind, container probes, watch, ingress, host inventory, stop,
# cached restart with persistence check, verdict.
# usage: 03-p1.sh <variant> [extra machine create flags...]
#   variant "virtio" = baseline (--net-backend virtio-net, no policy)
#   variant "blockpriv" = virtio-net + --block-private (native policy preview)
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD
V=${1:?variant}; shift
D=scripts/vm-spike/smolvm
R=$D/results-$V
SH=$ROOT/.spike-state/sh-$V
export HOME=/tmp/sv  # symlink to .spike-state/s; smolvm sockets must fit the 103-byte sun_path limit
S=$HOME/.smolvm/smolvm
M=jlp-smol-$V
mkdir -p "$D/logs"
exec > >(tee "$D/logs/03-$V.log") 2>&1
set -x
date -u; df -h / | tail -1
mkdir -p "$R" "$SH/jlp/out"
chmod 777 "$SH/jlp/out"
bin/vmprobe plan -candidate "smolvm-$V" -dir "$SH/fx" -guest-prefix /mnt/fx -port 47831 -alt-port 47832 -ipv6 "${IPV6:-disabled}" \
  -export "$SH/jlp:/mnt/jlp:rw" -out "$R/plan.json" || exit 1
cp bin/vmprobe-linux "$R/plan.json" "$SH/jlp/"
rsync -a --delete internal/embed/assets/ "$SH/jlp/assets/"
rsync -a --delete ../jailoc-lima/scripts/vm-spike/lima/tc/ "$SH/jlp/tc/"
cp ../jailoc/cmd/jailoc-vmprobe/sentinel.py "$D/sentinel.py"
/usr/bin/python3 "$D/sentinel.py" host "$R/hits.jsonl" 47831 47832 > "$D/logs/sentinel-$V.out" 2>&1 &
SPID=$!
sleep 1
bin/vmprobe selftest -plan "$R/plan.json" -out "$R/r-selftest.json" | tail -2

E() { "$S" machine exec --name "$M" "$@"; }
T() { local t0; t0=$(date +%s); "$@"; local rc=$?; echo "TIMING $(( $(date +%s) - t0 ))s rc=$rc :: $*"; return $rc; }
wait_docker() {
  local t0; t0=$(date +%s)
  for i in $(seq 1 120); do
    E -- docker info --format 'dockerd {{.ServerVersion}} {{.Driver}} {{.DockerRootDir}}' 2>/dev/null && { echo "TIMING docker-ready $(( $(date +%s) - t0 ))s after start returned"; return 0; }
    sleep 1
  done
  echo "dockerd not ready"; return 1
}

"$S" machine delete --name "$M" -f >/dev/null 2>&1
T "$S" machine create --name "$M" --image docker:29-dind --cpus 4 --mem 4096 --storage 30 --net --net-backend virtio-net \
  -p 47833:4096 -v "$SH/fx/rw:/mnt/fx/rw" -v "$SH/fx/ro:/mnt/fx/ro:ro" -v "$SH/jlp:/mnt/jlp" \
  -v /var/lib/docker -v /var/lib/containerd "$@" || exit 1
T "$S" machine start --name "$M" || { "$S" machine logs --name "$M" | tail -30; exit 1; }
wait_docker

# Step 2: guest inventory.
E -- sh -c 'id; uname -a; cat /proc/cmdline; cat /etc/os-release | head -2; mount; ip addr; ip route; ip -6 route; cat /etc/resolv.conf; ls -l /sys/class/net/*/device; ls /proc/config.gz; df -h /var/lib/docker /mnt/fx/rw' > "$R/guest-inventory.txt" 2>&1
E -- sh -c '(zcat /proc/config.gz 2>/dev/null || echo no-proc-config) | grep -E "NF_NAT=|NF_TABLES=|IP_NF_(NAT|FILTER)=|XT_MATCH_ADDRTYPE|BRIDGE=|VETH|OVERLAY_FS|IPV6=|VSOCK"' >> "$R/guest-inventory.txt" 2>&1

# Step 7 (part): probes as guest root and unprivileged user.
T E -- /mnt/jlp/vmprobe-linux guest -plan /mnt/jlp/plan.json -context guest-root -out /mnt/jlp/out/r-guest-root.json | tail -3
T E -u nobody -- /mnt/jlp/vmprobe-linux guest -plan /mnt/jlp/plan.json -context guest-user -out /mnt/jlp/out/r-guest-user.json | tail -3

if [ "$V" = virtio ]; then
  # Step 4: build the jailoc base image inside the guest.
  T E -- sh -c 'cd /mnt/jlp/assets && docker build -q -t jailoc-base:spike .' 2>&1 | tail -5
  E -- docker image ls jailoc-base:spike --format '{{.Repository}}:{{.Tag}} {{.Size}}'
  # Step 5: privileged Testcontainers workload and nested dind.
  T E -- docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock -v /mnt/jlp/tc:/tc -w /tc -e GOFLAGS=-mod=mod golang:1.26 \
    sh -c 'go mod tidy >/dev/null 2>&1; go test -count=1 -v ./...' > "$R/tc.log" 2>&1; tail -8 "$R/tc.log"
  E -- sh -c 'id=$(docker run -d --privileged docker:29-dind); for i in $(seq 1 40); do docker exec $id docker info --format "dind-ok {{.ServerVersion}} {{.Driver}}" 2>/dev/null && break; sleep 1; done; docker exec $id docker run --rm alpine echo nested-ok; echo "dind rc=$?"; docker logs $id 2>&1 | grep -E "level=(error|fatal)" | tail -3; docker rm -f $id >/dev/null'
  E -- sh -c 'docker volume create jlp-marker >/dev/null && docker run --rm -v jlp-marker:/m alpine sh -c "echo persisted-$(date +%s) > /m/marker && cat /m/marker"'
fi

# Container contexts.
T E -- docker run --rm -v /mnt/jlp:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-bridge -out /p/out/r-container-bridge.json | tail -3
T E -- docker run --rm --privileged --network=host -v /mnt/jlp:/p -v /mnt/fx:/mnt/fx alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-hostnet-privileged -out /p/out/r-container-hostnet-privileged.json | tail -3

# Fail-closed watch (no broker kill: the network stack lives in the VMM process).
T E -- /mnt/jlp/vmprobe-linux watch -plan /mnt/jlp/plan.json -context guest-root -duration 30s -out /mnt/jlp/out/r-watch.json -timeline /mnt/jlp/out/watch.jsonl | tail -2

# Ingress target and host-side check.
E -d -- /mnt/jlp/vmprobe-linux sentinel -port 4096 -name ingress -log /tmp/ingress.jsonl
sleep 2
bin/vmprobe ingress -plan "$R/plan.json" -port 47833 -out "$R/r-ingress.json" | tail -2

# Step 8: host processes, listeners and sockets.
ps -axo pid,user,ppid,comm,args | grep -E 'smolvm' | grep -v -E 'grep|03-p1' | cut -c1-220
for p in $(pgrep -f 'smolvm'); do
  echo "== pid $p"; lsof -nP -a -p "$p" -iTCP -sTCP:LISTEN; lsof -nP -a -p "$p" -iUDP; lsof -nP -a -p "$p" -U 2>/dev/null | awk 'NR>1{print $NF}' | sort -u | head -20
done
codesign -d --entitlements - "$HOME/.smolvm/smolvm-bin" 2>&1 | tail -12
"$S" machine data-dir --name "$M"; ls -la "$("$S" machine data-dir --name "$M")"

if [ "$V" = virtio ]; then
  # Step 6: stop, cached start, persistence.
  T "$S" machine stop --name "$M"
  T "$S" machine start --name "$M"
  wait_docker
  E -- docker image ls jailoc-base:spike --format 'image-survived {{.ID}}'
  E -- docker run --rm -v jlp-marker:/m alpine cat /m/marker
  E -- sh -c 'df -h /var/lib/docker; findmnt -T /var/lib/docker'
fi

T "$S" machine stop --name "$M"
cp "$SH"/jlp/out/r-*.json "$SH/jlp/out/watch.jsonl" "$R/"
sleep 1; kill $SPID
bin/vmprobe verdict -plan "$R/plan.json" -hits "$R/hits.jsonl" -out "$R/verdict.md" "$R"/r-*.json; echo "verdict rc=$?"
set +x
head -12 "$R/verdict.md"
du -sh "$HOME" "$SH"; df -h / | tail -1
echo ALL-DONE
