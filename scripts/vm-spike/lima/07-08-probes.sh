#!/usr/bin/env bash
# Steps 7-8: acceptance probes (baseline, default Lima networking) + host process/listener inventory.
set -uo pipefail
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/lima
R=$D/results
exec > >(tee "$D/logs/07-08.log") 2>&1
G() { limactl shell jlp-lima -- sudo bash -c "$1"; }
set -x
/usr/bin/python3 $D/sentinel.py host $R/hits.jsonl 47811 47812 > $D/logs/sentinel.out 2>&1 &
SPID=$!
echo "sentinel pid $SPID"
sleep 1
bin/vmprobe selftest -plan $R/plan.json -out $R/r-selftest.json
G 'mkdir -p /opt/jlp && chmod 755 /opt/jlp'
limactl copy bin/vmprobe-linux jlp-lima:/tmp/vmprobe-linux
limactl copy $R/plan.json jlp-lima:/tmp/plan.json
G 'install -m 755 /tmp/vmprobe-linux /opt/jlp/vmprobe-linux && install -m 644 /tmp/plan.json /opt/jlp/plan.json && chmod 777 /opt/jlp'
G 'ip -6 addr show scope global; ip -6 route show default; echo v6-check-done'
G '/opt/jlp/vmprobe-linux guest -plan /opt/jlp/plan.json -context guest-root -out /opt/jlp/r-guest-root.json | tail -3'
limactl shell jlp-lima -- /opt/jlp/vmprobe-linux guest -plan /opt/jlp/plan.json -context guest-user -out /opt/jlp/r-guest-user.json
G 'docker run --rm -v /opt/jlp:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-bridge -out /p/r-container-bridge.json'
G 'docker run --rm --privileged --network=host -v /opt/jlp:/p -v /mnt/fx:/mnt/fx alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-hostnet-privileged -out /p/r-container-hostnet-privileged.json'
G '/opt/jlp/vmprobe-linux watch -plan /opt/jlp/plan.json -context guest-root -duration 60s -out /opt/jlp/r-watch.json -timeline /opt/jlp/watch.jsonl'
# ingress: guest listener on 0.0.0.0:4096, Lima portForward -> host 127.0.0.1:47813
limactl shell jlp-lima -- bash -c 'nohup python3 -m http.server 4096 --bind 0.0.0.0 >/tmp/http.log 2>&1 &'
for i in $(seq 1 20); do nc -z 127.0.0.1 47813 && break; sleep 1; done
bin/vmprobe ingress -plan $R/plan.json -port 47813 -out $R/r-ingress.json
lsof -nP -iTCP:47813 -sTCP:LISTEN
G 'tar -C /opt/jlp -cf - r-guest-root.json r-guest-user.json r-container-bridge.json r-container-hostnet-privileged.json r-watch.json watch.jsonl' | tar -C $R -xf -
sleep 1
kill $SPID
bin/vmprobe verdict -plan $R/plan.json -hits $R/hits.jsonl -out $R/verdict.md $R/r-*.json; echo "verdict rc=$?"
# Step 8: host processes and listeners
ps -axo pid,user,comm,args | grep -E 'limactl|lima|vz|Virtualization' | grep -v grep | cut -c1-220
for p in $(pgrep -f 'limactl hostagent'); do lsof -nP -a -p $p -iTCP -sTCP:LISTEN; lsof -nP -a -p $p -iUDP; lsof -nP -a -p $p -U | awk '{print $NF}' | sort -u | head -20; done
ls -la ~/.lima/jlp-lima/
set +x
echo "== SUMMARY steps7-8"
head -12 $R/verdict.md
