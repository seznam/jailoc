#!/usr/bin/env bash
# P1 step 5 follow-up: nested dind with default and nftables firewall backends.
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD; D=scripts/vm-spike/go-microvm; SH=$ROOT/.spike-state/sh; VM=$ROOT/.spike-state/vm
exec > >(tee "$D/logs/05.log") 2>&1
set -x
(cd "$D/spike" && go build -o "$ROOT/.spike-state/spike" .) || exit 1
for n in 1; do
  echo dind > "$SH/jlp/phase"; rm -f "$SH/jlp/out-dind/DONE" "$SH/jlp/out-dind/marks.txt"
  t0=$(date +%s)
  "$ROOT/.spike-state/spike" docker:29-dind "$VM" "$SH" "$D/spike-init.sh" > "$D/logs/05-vm-$n.log" 2>&1 &
  VPID=$!
  for i in $(seq 1 600); do [ -f "$SH/jlp/out-dind/DONE" ] && break; kill -0 $VPID 2>/dev/null || break; sleep 0.5; done
  echo "cycle $n: launch=$t0 done=$(date +%s)"; cat "$SH/jlp/out-dind/marks.txt"
  grep -E 'image-survived|persisted-' "$SH/jlp/out-dind/init.log"
  t1=$(date +%s); kill -TERM $VPID; wait $VPID; echo "stop $(( $(date +%s) - t1 ))s"
  ls "$VM"
done
