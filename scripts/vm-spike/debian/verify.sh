#!/bin/sh
# Runs under systemd (jlp-verify.service) on every boot of the prepared rootfs.
O=/mnt/jlp/out
B=$(cat /mnt/jlp/boot)
exec > "$O/verify-$B.txt" 2>&1
set -x
date -u
uname -r
cat /etc/debian_version
readlink /proc/1/exe
cat /proc/1/comm
systemctl is-system-running  # "starting" while this verify job itself runs as part of boot
systemctl --failed --no-legend --no-pager
journalctl -b -o short-monotonic --no-pager | grep -m1 "Reached target multi-user.target"
systemd-analyze blame --no-pager | head -8
id
grep CapEff /proc/self/status
ip -br addr
ip route
networkctl list --no-pager
resolvectl dns
getent hosts deb.debian.org
apt-get update -q >/dev/null && apt-get install -y -q --no-install-recommends jq >/dev/null && jq --version
findmnt -n /var/lib/docker
journalctl -b -u var-lib-docker.mount --no-pager | tail -5
blkid /dev/vda
dmesg | grep -iE "ext4|vda" | tail -5
systemctl is-active docker containerd
journalctl -b -u docker --no-pager | grep -vE "^-- " | tail -15
docker info --format 'server={{.ServerVersion}} driver={{.Driver}} root={{.DockerRootDir}} cgroup={{.CgroupVersion}}/{{.CgroupDriver}}'
docker image ls --format '{{.Repository}}:{{.Tag}}'
docker run --rm --privileged alpine sh -c 'ip link add dummy0 type dummy && echo privileged-ok'
docker run --rm alpine wget -qO /dev/null https://example.com && echo container-egress-ok
/mnt/jlp/vmprobe-linux guest -plan /mnt/jlp/plan.json -context guest-root -checks net,dns,iface -timeout 5s -out "$O/r-systemd-$B.json" | tail -2
journalctl -b -p err --no-pager | tail -15
touch "$O/VERIFIED-$B"
systemctl poweroff
