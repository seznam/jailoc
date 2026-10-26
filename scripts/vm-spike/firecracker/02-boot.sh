#!/bin/bash
set -uo pipefail
base="$(dirname "$0")"
mkdir -p "$base/logs" "$base/results"
exec > >(tee "$base/logs/02.log") 2>&1
set -x
start=$(date +%s)
go build -o bin/vmprobe ./cmd/jailoc-vmprobe
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/vmprobe-linux ./cmd/jailoc-vmprobe
bin/vmprobe sentinel -port 47841 -port 47842 -name macos -log "$base/results/hits-macos.jsonl" > "$base/logs/macos-sentinel.log" 2>&1 &
printf '%s\n' "$!" > "$base/results/macos-sentinel.pid"
limactl shell jlp-fc-host -- bash /path/to/jailoc-firecracker/scripts/vm-spike/firecracker/02-boot-l1.sh
status=$?
if [ "$status" -eq 0 ]; then state=PASS; else state=BLOCKED; fi
printf 'Step 2 %s L2 boot/filesystems exit=%s elapsed=%ss; evidence=%s/logs/02.log\n' "$state" "$status" "$(( $(date +%s) - start ))" "$base" | tee -a "$base/PROGRESS.md"
printf 'Summary: boot=%s exit=%s elapsed=%ss\n' "$state" "$status" "$(( $(date +%s) - start ))"
exit "$status"
