#!/bin/bash
set -uo pipefail
base="$(dirname "$0")"
mkdir -p "$base/logs"
exec > >(tee "$base/logs/01.log") 2>&1
set -x
start=$(date +%s)
limactl shell jlp-fc-host -- bash /path/to/jailoc-firecracker/scripts/vm-spike/firecracker/01-install-l1.sh
status=$?
if [ "$status" -eq 0 ]; then state=PASS; else state=BLOCKED; fi
printf 'Step 1 %s install/artifacts exit=%s elapsed=%ss; evidence=%s/logs/01.log\n' "$state" "$status" "$(( $(date +%s) - start ))" "$base" | tee -a "$base/PROGRESS.md"
printf 'Summary: install=%s exit=%s elapsed=%ss\n' "$state" "$status" "$(( $(date +%s) - start ))"
exit "$status"
