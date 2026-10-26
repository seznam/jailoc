#!/usr/bin/env bash
# Step 4 retry: build the jailoc image alone in the guest (detached), after a clean VM restart.
set -uo pipefail
CL=/path/to/jailoc-firecracker
D=$CL/scripts/vm-spike/firecracker
exec >"$D/logs/04-build-retry.log" 2>&1
F() { limactl shell jlp-fc-host -- bash "$D/fc.sh" "$@"; }
set -x
F ssh "rm -r /var/lib/docker/assets 2>/dev/null; true"; F put /path/to/jailoc-firecracker/internal/embed/assets /var/lib/docker/
F ssh 'until docker info >/dev/null 2>&1; do sleep 1; done; docker ps -a --format "{{.Image}} {{.Status}}"; docker container prune -f >/dev/null
date +%s > /tmp/build.start
cd /var/lib/docker/assets && setsid -f bash -c "docker build --progress=plain -t jailoc-base:spike . >/tmp/build.log 2>&1; echo rc=\$? >> /tmp/build.log; date +%s > /tmp/build.end" </dev/null >/dev/null 2>&1
echo build-started'
while :; do
  sleep 60
  F ssh 'test -f /tmp/build.end && { tail -30 /tmp/build.log; echo "build seconds $(( $(cat /tmp/build.end) - $(cat /tmp/build.start) ))"; docker image ls jailoc-base:spike --format "{{.Repository}}:{{.Tag}} {{.Size}}"; exit 0; }; tail -1 /tmp/build.log | cut -c1-120; exit 1' && break
done
echo DONE
