#!/usr/bin/env bash
# P1 step 6 for go-microvm: stop/start cycle with WithCleanDataDir (rootfs is re-cloned from the
# image cache each boot); Docker state lives on the loop-mounted ext4 image on the dstate share.
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD; D=scripts/vm-spike/go-microvm; SH=$ROOT/.spike-state/sh; VM=$ROOT/.spike-state/vm
exec > >(tee "$D/logs/04.log") 2>&1
set -x
(cd "$D/spike" && go build -o "$ROOT/.spike-state/spike" .) || exit 1
for n in 1 2; do
  echo persist > "$SH/jlp/phase"; rm -f "$SH/jlp/out-persist/DONE" "$SH/jlp/out-persist/marks.txt"
  t0=$(date +%s)
  "$ROOT/.spike-state/spike" docker:29-dind "$VM" "$SH" "$D/spike-init.sh" > "$D/logs/04-vm-$n.log" 2>&1 &
  VPID=$!
  for i in $(seq 1 300); do [ -f "$SH/jlp/out-persist/DONE" ] && break; kill -0 $VPID 2>/dev/null || break; sleep 0.5; done
  echo "cycle $n: launch=$t0 done=$(date +%s)"; cat "$SH/jlp/out-persist/marks.txt"
  grep -E 'image-survived|persisted-' "$SH/jlp/out-persist/init.log"
  t1=$(date +%s); kill -TERM $VPID; wait $VPID; echo "stop $(( $(date +%s) - t1 ))s"
  ls "$VM"
done
