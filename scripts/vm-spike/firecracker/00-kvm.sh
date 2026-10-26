#!/bin/bash
set -uo pipefail
base="$(dirname "$0")"
mkdir -p "$base/logs"
exec > >(tee "$base/logs/00.log") 2>&1
set -x
start=$(date +%s)
python3 -c 'import subprocess,sys; args=["limactl","shell","jlp-fc-host","--","sh","-lc","uname -a; ls -l /dev/kvm; test -c /dev/kvm; mount | grep jailoc-firecracker"]; sys.exit(subprocess.run(args,timeout=65).returncode)' 
status=$?
if [ "$status" -eq 0 ]; then
  printf 'Step 0 PASS /dev/kvm accessible; elapsed=%ss; evidence=%s/logs/00.log\n' "$(( $(date +%s) - start ))" "$base" | tee -a "$base/PROGRESS.md"
else
  printf 'Step 0 BLOCKED /dev/kvm not confirmed after two attempts (first timed out); second exit=%s elapsed=%ss; evidence=%s/logs/00.log\n' "$status" "$(( $(date +%s) - start ))" "$base" | tee -a "$base/PROGRESS.md"
fi
printf 'Summary: step0 status=%s; elapsed=%ss; L1=jlp-fc-host\n' "$status" "$(( $(date +%s) - start ))"
exit "$status"
