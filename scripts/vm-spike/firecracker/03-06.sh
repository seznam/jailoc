#!/usr/bin/env bash
# Steps 3-6 for Firecracker (L2 guest inside the jlp-fc-host L1 Lima VM).
# 3 rootful dockerd on the /dev/vdb ext4 drive, 4 build jailoc image in guest,
# 5 privileged Testcontainers + dind, 6 persistence across VM stop/start.
set -uo pipefail
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/firecracker
CL=/path/to/jailoc-firecracker
mkdir -p "$D/logs"
exec > >(tee "$D/logs/03-06.log") 2>&1
F() { limactl shell jlp-fc-host -- bash "$CL/$D/fc.sh" "$@"; }
G() { F ssh "$1"; }
set -x
# Step 3: Docker CE from the Docker apt repo. The Firecracker CI rootfs ships without gpgv,
# so bootstrap gpgv from an unauthenticated index once, then use signed indexes.
t0=$(date +%s)
G 'set -e; export DEBIAN_FRONTEND=noninteractive
  apt-get -q -o Acquire::AllowInsecureRepositories=true update >/dev/null
  apt-get install -y -q --allow-unauthenticated gpgv >/dev/null
  apt-get update -q >/dev/null
  apt-get install -y -q ca-certificates curl >/dev/null
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  echo "deb [arch=arm64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list
  apt-get update -q >/dev/null
  apt-get install -y -q -o Dpkg::Options::=--force-confold docker-ce docker-ce-cli containerd.io docker-buildx-plugin >/dev/null
  dpkg --configure -a --force-confold
  systemctl enable --now docker'; rc3=$?
t1=$(date +%s)
G 'docker version --format "{{.Server.Version}}"; docker info --format "driver={{.Driver}} root={{.DockerRootDir}} cgroup={{.CgroupVersion}} fw={{.FirewallBackend}}"; findmnt -T /var/lib/docker; ps -o user,cmd -C dockerd'
# Step 4
F put "$CL/internal/embed/assets" /tmp/assets
F put "$CL/$D/tc" /tmp/tc
t2=$(date +%s)
G 'cd /tmp/assets && docker build -q -t jailoc-base:spike . 2>&1 | tail -20'; rc4=$?
t3=$(date +%s)
G 'docker image ls jailoc-base:spike --format "{{.Repository}}:{{.Tag}} {{.Size}}"'
# Step 5
t4=$(date +%s)
G 'docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock -v /tmp/tc:/tc -w /tc -e GOFLAGS=-mod=mod golang:1.26 sh -c "go mod tidy >/dev/null 2>&1; go test -count=1 -v ./... 2>&1 | tail -15"'; rc5=$?
t5=$(date +%s)
G 'id=$(docker run -d --privileged docker:dind); for i in $(seq 1 60); do docker exec $id docker info --format "dind-ok {{.ServerVersion}} {{.Driver}}" 2>/dev/null && break; sleep 1; done; docker exec $id docker run --rm alpine echo nested-ok; docker rm -f $id >/dev/null'; rc5b=$?
t6=$(date +%s)
# Step 6
G 'docker volume create jlp-marker >/dev/null && docker run --rm -v jlp-marker:/m alpine sh -c "echo persisted-$(date +%s) > /m/marker && cat /m/marker"'
t7=$(date +%s)
F stop
t8=$(date +%s)
F boot
until G 'docker info >/dev/null 2>&1'; do sleep 0.5; done
t9=$(date +%s)
G 'docker image ls jailoc-base:spike --format "image-survived {{.ID}}"; docker run --rm -v jlp-marker:/m alpine cat /m/marker; systemctl is-active docker; systemd-analyze | head -1'
set +x
echo "== SUMMARY steps3-6"
echo "docker install rc=$rc3 $((t1-t0))s; build rc=$rc4 $((t3-t2))s; tc rc=$rc5 $((t5-t4))s; dind rc=$rc5b $((t6-t5))s; stop $((t8-t7))s; cached start->docker info $((t9-t8))s"
