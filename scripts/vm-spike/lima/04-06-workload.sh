#!/usr/bin/env bash
# Steps 4-6: build jailoc image in guest, privileged Testcontainers + dind smoke, persistence across restart.
set -uo pipefail
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/lima
exec > >(tee "$D/logs/04-06.log") 2>&1
G() { limactl shell jlp-lima -- sudo bash -c "$1"; }
set -x
limactl copy -r internal/embed/assets jlp-lima:/tmp/assets
limactl copy -r "$D/tc" jlp-lima:/tmp/tc
# Step 4
t0=$(date +%s)
G 'cd /tmp/assets && docker build -q -t jailoc-base:spike . 2>&1 | tail -20'; rc4=$?
t1=$(date +%s)
G 'docker image ls jailoc-base:spike --format "{{.Repository}}:{{.Tag}} {{.Size}}"'
# Step 5
t2=$(date +%s)
G 'docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock -v /tmp/tc:/tc -w /tc -e GOFLAGS=-mod=mod golang:1.26 sh -c "go mod tidy >/dev/null 2>&1; go test -count=1 -v ./... 2>&1 | tail -15"'; rc5=$?
t3=$(date +%s)
G 'id=$(docker run -d --privileged docker:dind); for i in $(seq 1 30); do docker exec $id docker info --format "dind-ok {{.ServerVersion}} {{.Driver}}" 2>/dev/null && break; sleep 1; done; docker exec $id docker run --rm alpine echo nested-ok; docker rm -f $id >/dev/null'; rc5b=$?
t4=$(date +%s)
# Step 6
G 'docker volume create jlp-marker >/dev/null && docker run --rm -v jlp-marker:/m alpine sh -c "echo persisted-$(date +%s) > /m/marker && cat /m/marker"'
limactl stop jlp-lima
t5=$(date +%s)
limactl start --tty=false jlp-lima >/dev/null 2>&1
until limactl shell jlp-lima -- sudo docker info >/dev/null 2>&1; do sleep 0.5; done
t6=$(date +%s)
G 'docker image ls jailoc-base:spike --format "image-survived {{.ID}}"; docker run --rm -v jlp-marker:/m alpine cat /m/marker; systemctl is-active docker'
set +x
echo "== SUMMARY steps4-6"
echo "build rc=$rc4 $((t1-t0))s; tc rc=$rc5 $((t3-t2))s; dind rc=$rc5b $((t4-t3))s; cached start->docker info $((t6-t5))s"
df -h / | tail -1; du -sh ~/.lima/jlp-lima
