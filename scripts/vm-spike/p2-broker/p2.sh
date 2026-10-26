#!/usr/bin/env bash
# P2 network proof: go-microvm VM whose only NIC is served by jailoc-netbroker.
# Runs vm-acceptance/2 probes from all contexts, then exercises the broker
# lifecycle (invalid policy reload, restore, SIGKILL, restart) during a guest
# watch, a boot against a dead broker, and a cached start with a boot-time watch.
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD
D=scripts/vm-spike/p2-broker
R=$D/results
SH=$ROOT/.spike-state/sh-p2
VM=$ROOT/.spike-state/vm
SOCK=/tmp/jlp-nb.sock
LAN=${LAN:?set LAN to the address of a second machine running the LAN sentinel}
LAN_VM=${LAN_VM:?set LAN_VM to the Lima instance that runs the LAN sentinel}
mkdir -p "$D/logs"
exec > >(tee "$D/logs/p2.log") 2>&1
set -x
date -u
mkdir -p "$R" "$SH/jlp" "$SH/dstate"
rm -f "$R"/*
[ -d "$SH/fx" ] && rm -r "$SH/fx"
bin/vmprobe2 plan -candidate go-microvm+netbroker -dir "$SH/fx" -guest-prefix /mnt/fx -port 47851 -alt-port 47852 -ipv6 disabled \
  -lan-sentinel $LAN -exception $LAN/32,tcp,47851 \
  -export "$SH/jlp:/mnt/jlp:rw" -export "$SH/dstate:/mnt/dstate:rw" -out "$R/plan.json" || exit 1
cp bin/vmprobe2-linux "$SH/jlp/vmprobe-linux"
cp "$R/plan.json" "$SH/jlp/plan.json"
printf '{"exceptions":[{"cidr":"%s/32","proto":"tcp","ports":["47851"]}]}\n' $LAN > "$R/policy-good.json"
cp "$R/policy-good.json" "$R/policy.json"

# Sentinels: host (Apple-signed python for the macOS firewall) and LAN (Lima instance $LAN_VM).
/usr/bin/python3 cmd/jailoc-vmprobe/sentinel.py host "$ROOT/$R/hits-host.jsonl" 47851 47852 > "$D/logs/sentinel.out" 2>&1 &
SPID=$!
limactl copy bin/vmprobe2-linux "$LAN_VM":/tmp/vmprobe-linux
limactl copy "$R/plan.json" "$LAN_VM":/tmp/plan.json
limactl shell "$LAN_VM" -- bash -c 'sudo systemctl stop jlp-lan-sentinel 2>/dev/null; sudo systemctl reset-failed jlp-lan-sentinel 2>/dev/null; sudo rm -f /tmp/hits-lan.jsonl; sudo systemd-run --unit jlp-lan-sentinel /tmp/vmprobe-linux sentinel -plan /tmp/plan.json -name lan -log /tmp/hits-lan.jsonl'
sleep 2
bin/vmprobe2 selftest -plan "$R/plan.json" -out "$R/r-selftest.json" | tail -2

BPID=
broker() {
  bin/jailoc-netbroker -socket $SOCK -policy "$R/policy.json" -forward 127.0.0.1:47853=4096 -log "$ROOT/$R/broker.jsonl" >> "$D/logs/broker.err" 2>&1 &
  BPID=$!
  for _ in $(seq 1 50); do [ -S $SOCK ] && break; sleep 0.1; done
  echo "broker pid=$BPID at $(date +%s)"
}
VPID=
boot() { # phase timeout
  echo "$1" > "$SH/jlp/phase"
  [ -d "$SH/jlp/out-$1" ] && rm -r "$SH/jlp/out-$1"
  local t0; t0=$(date +%s)
  "$ROOT/.spike-state/spike-p2" docker:29-dind "$VM" "$SH" "$D/p2-init.sh" $SOCK > "$D/logs/vm-$1.log" 2>&1 &
  VPID=$!
  for _ in $(seq 1 "$2"); do
    [ -f "$SH/jlp/out-$1/DONE" ] && break
    kill -0 $VPID 2>/dev/null || { echo "spike exited early"; break; }
    sleep 1
  done
  echo "phase $1: done after $(( $(date +%s) - t0 ))s (launch at $t0)"
}

# 1. Boot against a dead broker: stale socket, nothing listening.
broker; kill -9 $BPID; wait $BPID 2>/dev/null; ls -la $SOCK
echo dead > "$SH/jlp/phase"
t0=$(date +%s)
timeout 60 "$ROOT/.spike-state/spike-p2" docker:29-dind "$VM" "$SH" "$D/p2-init.sh" $SOCK > "$D/logs/vm-deadbroker.log" 2>&1
echo "dead-broker boot rc=$? after $(( $(date +%s) - t0 ))s"; tail -5 "$D/logs/vm-deadbroker.log"
pkill -f go-microvm-runner; sleep 2

# 2. Full run.
broker
echo full > "$SH/jlp/phase"
[ -d "$SH/jlp/out-full" ] && rm -r "$SH/jlp/out-full"
T0=$(date +%s)
"$ROOT/.spike-state/spike-p2" docker:29-dind "$VM" "$SH" "$D/p2-init.sh" $SOCK > "$D/logs/vm-full.log" 2>&1 &
VPID=$!
for _ in $(seq 1 2400); do [ -f "$SH/jlp/out-full/WATCH" ] && break; kill -0 $VPID 2>/dev/null || break; sleep 1; done
W0=$(date +%s); echo "watch started ~$W0"
bin/vmprobe2 ingress -plan "$R/plan.json" -port 47853 -out "$R/r-ingress.json" | tail -2
sleep 10
echo '{"exceptions": [' > "$R/policy.json"; kill -HUP $BPID; echo "invalid policy reload at $(date +%s) (+$(( $(date +%s) - W0 ))s)"
sleep 20
cp "$R/policy-good.json" "$R/policy.json"; kill -HUP $BPID; echo "policy restored at $(date +%s) (+$(( $(date +%s) - W0 ))s)"
sleep 20
# Step 8 inventory while everything runs.
ps -axo pid,user,args | grep -E 'jailoc-netbroker|go-microvm-runner|spike-p2' | grep -v grep | cut -c1-200
for p in $BPID $(pgrep -f go-microvm-runner); do
  echo "== pid $p"; lsof -nP -a -p "$p" -iTCP -sTCP:LISTEN; lsof -nP -a -p "$p" -iUDP; lsof -nP -a -p "$p" -U | awk 'NR>1{print $NF}' | sort -u
done
kill -9 $BPID; echo "broker SIGKILL at $(date +%s) (+$(( $(date +%s) - W0 ))s)"
sleep 15
broker; echo "broker restarted (+$(( $(date +%s) - W0 ))s)"
for _ in $(seq 1 600); do [ -f "$SH/jlp/out-full/DONE" ] && break; sleep 1; done
echo "full phase done after $(( $(date +%s) - T0 ))s"
grep -E '"event":"(nic|policy)"' "$R/broker.jsonl"
kill -TERM $VPID; wait $VPID; tail -3 "$D/logs/vm-full.log"

# 3. Cached start with a boot-time watch (broker already running).
boot cached 300
cat "$SH/jlp/out-cached/marks.txt"
kill -TERM $VPID; wait $VPID

kill $BPID; wait $BPID 2>/dev/null
cp "$SH"/jlp/out-full/r-*.json "$SH/jlp/out-full/watch.jsonl" "$SH/jlp/out-full/marks.txt" "$R/"
cp "$SH/jlp/out-cached/r-watch.json" "$R/r-watch-cached.json"
cp "$SH/jlp/out-cached/watch.jsonl" "$R/watch-cached.jsonl"
cp "$SH/jlp/out-cached/marks.txt" "$R/marks-cached.txt"
limactl shell "$LAN_VM" -- sudo cat /tmp/hits-lan.jsonl > "$R/hits-lan.jsonl"
limactl shell "$LAN_VM" -- sudo systemctl stop jlp-lan-sentinel
sleep 1; kill $SPID
bin/vmprobe2 verdict -plan "$R/plan.json" -hits "$R/hits-host.jsonl" -hits "$R/hits-lan.jsonl" -out "$R/verdict.md" "$R"/r-*.json; echo "verdict rc=$?"
set +x
head -12 "$R/verdict.md"
echo P2-DONE
