#!/usr/bin/env bash
# P1 steps 2-8 for go-microvm: boot, guest probes, dockerd on a loop-mounted ext4 image,
# in-guest image build, privileged Testcontainers + dind, container probes, watch,
# ingress, host inventory, stop, cached restart with persistence check, verdict.
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD
D=scripts/vm-spike/go-microvm
R=$D/results
SH=$ROOT/.spike-state/sh
VM=$ROOT/.spike-state/vm
exec > >(tee "$D/logs/03.log") 2>&1
set -x
date -u; df -h / | tail -1
if [ -d "$R" ] && [ ! -d "$D/results-run1-partial" ]; then mv "$R" "$D/results-run1-partial"; fi
mkdir -p "$R" "$SH/jlp" "$SH/dstate"
bin/vmprobe plan -candidate go-microvm -dir "$SH/fx" -guest-prefix /mnt/fx -port 47821 -alt-port 47822 -ipv6 disabled \
  -export "$SH/jlp:/mnt/jlp:rw" -export "$SH/dstate:/mnt/dstate:rw" -out "$R/plan.json" || exit 1
cp bin/vmprobe-linux "$R/plan.json" "$SH/jlp/"
rsync -a --delete internal/embed/assets/ "$SH/jlp/assets/"
rsync -a --delete ../jailoc-lima/scripts/vm-spike/lima/tc/ "$SH/jlp/tc/"
cp ../jailoc/cmd/jailoc-vmprobe/sentinel.py "$D/sentinel.py"
/usr/bin/python3 "$D/sentinel.py" host "$R/hits.jsonl" 47821 47822 > "$D/logs/sentinel.out" 2>&1 &
SPID=$!
sleep 1
bin/vmprobe selftest -plan "$R/plan.json" -out "$R/r-selftest.json" | tail -2
(cd "$D/spike" && go build -o "$ROOT/.spike-state/spike" .) || exit 1

boot() { # phase timeout
  echo "$1" > "$SH/jlp/phase"
  rm -f "$SH/jlp/out-$1/DONE"
  local t0; t0=$(date +%s)
  "$ROOT/.spike-state/spike" docker:29-dind "$VM" "$SH" "$D/spike-init.sh" > "$D/logs/03-vm-$1.log" 2>&1 &
  VPID=$!
  for i in $(seq 1 "$2"); do
    [ -f "$SH/jlp/out-$1/DONE" ] && break
    kill -0 $VPID 2>/dev/null || { echo "spike exited early"; break; }
    sleep 1
  done
  echo "phase $1: done after $(( $(date +%s) - t0 ))s (launch at $t0)"
  BOOT_T0=$t0
}

boot full 2400
cat "$SH/jlp/out-full/marks.txt"
bin/vmprobe ingress -plan "$R/plan.json" -port 47823 -out "$R/r-ingress.json" | tail -2
# Step 8: host processes, listeners and sockets.
ps -axo pid,user,comm,args | grep -E 'go-microvm|\.spike-state/spike' | grep -v grep | cut -c1-200
for p in $(pgrep -f 'go-microvm-runner') $VPID; do
  lsof -nP -a -p "$p" -iTCP -sTCP:LISTEN; lsof -nP -a -p "$p" -iUDP; lsof -nP -a -p "$p" -U | awk 'NR>1{print $NF}' | sort -u | head -20
done
codesign -d --entitlements - "$VM/go-microvm-runner" 2>&1 | tail -15
ls -la "$VM"; cat "$VM/go-microvm-state.json"; echo
t0=$(date +%s); kill -TERM $VPID; wait $VPID; echo "stop $(( $(date +%s) - t0 ))s"
tail -3 "$D/logs/03-vm-full.log"

boot persist 600
cat "$SH/jlp/out-persist/marks.txt"; echo "launch=$BOOT_T0"
tail -5 "$SH/jlp/out-persist/init.log"
kill -TERM $VPID; wait $VPID

cp "$SH"/jlp/out-full/r-*.json "$SH/jlp/out-full/watch.jsonl" "$R/"
sleep 1; kill $SPID
bin/vmprobe verdict -plan "$R/plan.json" -hits "$R/hits.jsonl" -out "$R/verdict.md" "$R"/r-*.json; echo "verdict rc=$?"
set +x
head -12 "$R/verdict.md"
du -sh "$VM" "$SH/dstate"; df -h / | tail -1
