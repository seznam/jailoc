#!/bin/bash
set -uo pipefail
BASE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$BASE/../../.." && pwd)"
mkdir -p "$BASE/logs" "$BASE/results" "$ROOT/.spike-state/fx" "$ROOT/.spike-state/vm"
exec > >(tee "$BASE/logs/02.log") 2>&1
set -x
cd "$ROOT"
date -u
df -h /
bin/vmprobe plan -candidate go-microvm -dir "$ROOT/.spike-state/fx" -guest-prefix /mnt/fx -port 47821 -alt-port 47822 -ipv6 disabled -out "$BASE/results/plan.json"
cp "$BASE/results/plan.json" "$ROOT/.spike-state/fx/rw/plan.json"
cp bin/vmprobe-linux "$ROOT/.spike-state/fx/rw/vmprobe-linux"
bin/vmprobe sentinel -plan "$BASE/results/plan.json" -name host -log "$BASE/results/hits.jsonl" &
sentinel_pid=$!
printf '%s\n' "$sentinel_pid" > "$BASE/results/sentinel.pid"
sleep 2
bin/vmprobe selftest -plan "$BASE/results/plan.json" -out "$BASE/results/r-selftest.json" || true
cp "$ROOT/.spike-state/go-microvm-runner" "$ROOT/.spike-state/vm/go-microvm-runner"
codesign --entitlements "$BASE/upstream/assets/entitlements.plist" --force -s - "$ROOT/.spike-state/vm/go-microvm-runner"
(cd "$BASE/spike" && go build -o "$ROOT/.spike-state/spike" .)
start=$(date +%s)
"$ROOT/.spike-state/spike" docker:27-dind "$ROOT/.spike-state/vm" "$ROOT/.spike-state/fx" shell > "$BASE/logs/02-vm.log" 2>&1 &
spike_pid=$!
printf '%s\n' "$spike_pid" > "$BASE/results/spike.pid"
sleep 70
ps -p "$spike_pid" -o pid,user,command || true
ps -axo pid,user,command | rg 'go-microvm-runner|\.spike-state/spike' || true
lsof -nP -iTCP -sTCP:LISTEN | rg '4782[123]|COMMAND' || true
lsof -nP -iUDP | rg 'go-microvm|COMMAND' || true
bin/vmprobe ingress -plan "$BASE/results/plan.json" -port 47823 -out "$BASE/results/r-ingress.json" || true
ls -lh "$ROOT/.spike-state/fx/rw" "$ROOT/.spike-state/vm" || true
if test -f "$ROOT/.spike-state/fx/rw/r-guest-root.json"; then cp "$ROOT/.spike-state/fx/rw/r-guest-root.json" "$BASE/results/"; fi
if test -f "$ROOT/.spike-state/fx/rw/r-guest-user.json"; then cp "$ROOT/.spike-state/fx/rw/r-guest-user.json" "$BASE/results/"; fi
bin/vmprobe verdict -plan "$BASE/results/plan.json" -hits "$BASE/results/hits.jsonl" -out "$BASE/results/verdict.md" "$BASE"/results/r-*.json || true
kill "$spike_pid" "$sentinel_pid" 2>/dev/null || true
wait "$spike_pid" 2>/dev/null || true
wait "$sentinel_pid" 2>/dev/null || true
date -u
du -sh "$ROOT/.spike-state/vm" "$ROOT/.spike-state/fx" "$BASE/results"
df -h /
printf 'Step 2: elapsed=%ss; evidence logs/02.log and logs/02-vm.log\n' "$(( $(date +%s) - start ))"
printf 'Step 2: guest-root=%s dockerd-attempt=%s verdict=%s\n' "$(test -f "$BASE/results/r-guest-root.json" && printf yes || printf no)" "$(test -f "$ROOT/.spike-state/fx/rw/dockerd-attempt.txt" && printf yes || printf no)" "$(test -f "$BASE/results/verdict.md" && printf yes || printf no)"
printf '2-8. Boot/probes: see logs/02.log and logs/02-vm.log; elapsed %ss; guest-root %s; dockerd %s; verdict %s.\n' "$(( $(date +%s) - start ))" "$(test -f "$BASE/results/r-guest-root.json" && printf yes || printf no)" "$(test -f "$ROOT/.spike-state/fx/rw/dockerd-attempt.txt" && printf yes || printf no)" "$(test -f "$BASE/results/verdict.md" && printf yes || printf no)" >> "$BASE/PROGRESS.md"
