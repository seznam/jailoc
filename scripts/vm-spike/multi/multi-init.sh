#!/bin/sh
# Guest init for the multi-workspace spike. Workspace name in /mnt/jlp/name.
# Order: network, peer sentinel, wait for the host's "go", probes in four
# contexts, dockerd on the virtio-blk data disk, ingress target, short watch.
J=/mnt/jlp
mkdir -p /mnt/fx/rw /mnt/fx/ro $J
mount -t virtiofs fx-rw /mnt/fx/rw
mount -t virtiofs -o ro fx-ro /mnt/fx/ro
mount -t virtiofs jlp $J
O=$J/out
mkdir -p $O
exec >$O/init.log 2>&1
set -x
NAME=$(cat $J/name)
mark() { echo "$(date +%s) $*" >> $O/marks.txt; touch "$O/$1"; }
mark BOOT
ip link set eth0 up
udhcpc -i eth0 -n -q -t 10
ip addr show eth0; ip route; cat /etc/resolv.conf
mark NET

# Peer sentinel: the other workspace probes this guest and must never reach it.
$J/vmprobe-linux sentinel -plan $J/plan.json -name "ws-$NAME" -log $O/hits-guest.jsonl &
sleep 1
mark READY
while [ ! -f $J/go ]; do sleep 1; done

$J/vmprobe-linux guest -plan $J/plan.json -context guest-root -out $O/r-guest-root.json | tail -3
mkdir -p /tmp/u && chmod 777 /tmp/u
su nobody -s /bin/sh -c "$J/vmprobe-linux guest -plan $J/plan.json -context guest-user -out /tmp/u/r-guest-user.json" | tail -3
cp /tmp/u/r-guest-user.json $O/
mark PROBES

# Docker state on the virtio-blk data disk, formatted on first boot.
apk add -q --no-cache e2fsprogs nftables
[ -n "$(blkid /dev/vda)" ] || mkfs.ext4 -q -F /dev/vda
mkdir -p /var/lib/docker
mount /dev/vda /var/lib/docker
grep ' /var/lib/docker ' /proc/mounts
sysctl -w net.ipv4.ip_forward=1
dockerd --data-root /var/lib/docker --firewall-backend=nftables > $O/dockerd.log 2>&1 &
for i in $(seq 1 60); do docker info >/dev/null 2>&1 && break; sleep 1; done
mark DOCKERD
docker run --rm -v $J:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-bridge -out /p/out/r-container-bridge.json | tail -3
docker run --rm --privileged --network=host -v $J:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-hostnet-privileged -out /p/out/r-container-hostnet-privileged.json | tail -3
mark CONTAINERS

(cd /tmp && busybox httpd -f -p 0.0.0.0:4096) &
mark HTTPD
$J/vmprobe-linux watch -plan $J/plan.json -context guest-root -interval 1s -duration 30s -timeout 2s \
  -out $O/r-watch.json -timeline $O/watch.jsonl
mark DONE
while :; do sleep 3600; done
