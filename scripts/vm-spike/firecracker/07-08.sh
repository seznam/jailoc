#!/usr/bin/env bash
# Steps 7-8 for Firecracker: acceptance probes + host inventory. Usage: 07-08.sh baseline|policy
# L1 (jlp-fc-host) is the host; the Mac (10.0.0.73, and 192.168.5.2 = Mac loopback via Lima usernet)
# hosts the LAN sentinels.
set -uo pipefail
V=${1:?variant}
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/firecracker
CL=/path/to/jailoc-firecracker
R=$D/results-$V
mkdir -p "$R"
rm -f "$R"/*.json "$R"/*.jsonl "$R"/verdict.md
cp "$D/results/plan.json" "$R/plan.json"
exec > >(tee "$D/logs/07-08-$V.log") 2>&1
F() { limactl shell jlp-fc-host -- bash "$CL/$D/fc.sh" "$@"; }
G() { F ssh "$1"; }
L() { limactl shell jlp-fc-host -- bash -c "$1"; }
set -x
case "$V" in policy) F policy on || exit 1;; *) F policy off;; esac
L 'sudo nft list ruleset | grep -E "^table|hook (forward|input)"'
# Sentinels
/usr/bin/python3 -c "import subprocess; subprocess.Popen(['/usr/bin/python3','/path/to/jailoc/cmd/jailoc-vmprobe/sentinel.py','mac','$CL/$R/hits-mac.jsonl','47841','47842'],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL,start_new_session=True)"
L "setsid -f $CL/bin/vmprobe-linux sentinel -plan $CL/$R/plan.json -name l1 -log $CL/$R/hits-l1.jsonl </dev/null >/dev/null 2>&1"
sleep 1
L "$CL/bin/vmprobe-linux selftest -plan $CL/$R/plan.json -out $CL/$R/r-selftest.json | tail -3"
# Guest probes
G 'mkdir -p /opt/jlp && chmod 777 /opt/jlp && rm -f /opt/jlp/r-*.json /opt/jlp/watch.jsonl'
F put "$CL/bin/vmprobe-linux" /opt/jlp/vmprobe-linux
F put "$CL/$R/plan.json" /opt/jlp/plan.json
G 'chmod 755 /opt/jlp/vmprobe-linux; chmod 644 /opt/jlp/plan.json; ip -6 addr show scope global; ip -6 route show default; echo v6-check-done'
G '/opt/jlp/vmprobe-linux guest -timeout 10s -plan /opt/jlp/plan.json -context guest-root -out /opt/jlp/r-guest-root.json'
G 'runuser -u ubuntu -- /opt/jlp/vmprobe-linux guest -timeout 10s -plan /opt/jlp/plan.json -context guest-user -out /opt/jlp/r-guest-user.json'
G 'docker run --rm -v /opt/jlp:/p alpine /p/vmprobe-linux guest -timeout 10s -plan /p/plan.json -context container-bridge -out /p/r-container-bridge.json'
G 'docker run --rm --privileged --network=host -v /opt/jlp:/p -v /mnt/fx:/mnt/fx alpine /p/vmprobe-linux guest -timeout 10s -plan /p/plan.json -context container-hostnet-privileged -out /p/r-container-hostnet-privileged.json'
# Fail-closed watch: policy variant reloads the ruleset atomically 10x and restarts NFS during the watch.
G 'setsid -f /opt/jlp/vmprobe-linux watch -timeout 5s -plan /opt/jlp/plan.json -context guest-root -duration 60s -out /opt/jlp/r-watch.json -timeline /opt/jlp/watch.jsonl </dev/null >/dev/null 2>&1'
if [ "$V" = policy ]; then
  for i in $(seq 1 10); do sleep 4; F policy reload; done
fi
sleep 65
G 'until test -s /opt/jlp/r-watch.json; do sleep 1; done; echo watch-done'
# Ingress: guest listener on 0.0.0.0:4096, L1 socat on 127.0.0.1:47843 (what a jailoc helper would publish).
G 'setsid -f python3 -m http.server 4096 --bind 0.0.0.0 </dev/null >/tmp/http.log 2>&1'
L 'setsid -f socat TCP-LISTEN:47843,bind=127.0.0.1,fork,reuseaddr TCP:172.16.0.2:4096 </dev/null >/dev/null 2>&1; sleep 1'
L "$CL/bin/vmprobe-linux ingress -plan $CL/$R/plan.json -port 47843 -out $CL/$R/r-ingress.json | tail -3"
L 'pkill -f "socat TCP-LISTEN:47843"'
# Collect
L "rm -r /tmp/jlp-get 2>/dev/null; mkdir -p /tmp/jlp-get"
F get '/opt/jlp/r-*.json' /tmp/jlp-get/
F get /opt/jlp/watch.jsonl /tmp/jlp-get/
L "sudo chown -R lima /tmp/jlp-get; cp /tmp/jlp-get/* $CL/$R/"
sleep 1
L 'pkill -f "vmprobe-linux sentinel"'
pkill -f "sentinel.py mac $CL/$R/hits-mac.jsonl"
L "$CL/bin/vmprobe-linux verdict -plan $CL/$R/plan.json -hits $CL/$R/hits-l1.jsonl -hits $CL/$R/hits-mac.jsonl -out $CL/$R/verdict.md $CL/$R/r-*.json; echo verdict rc=\$?"
# Step 8: host inventory (L1)
L 'ps -eo pid,user,args | grep -E "[f]irecracker|[n]fsd|[r]pc" | cut -c1-200; sudo ls -la /var/lib/jlp-fc/*.sock; sudo ss -lntup | grep -vE "127.0.0.53|lima-guestagent" | cut -c1-160; ip -br link'
set +x
echo "== SUMMARY steps7-8 $V"
head -12 "$R/verdict.md"
