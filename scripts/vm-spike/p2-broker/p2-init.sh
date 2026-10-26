#!/bin/sh
# Guest init for the P2 broker proof (go-microvm + jailoc-netbroker).
# Phase from /mnt/jlp/phase:
#   full   - guest probes (root/user), dockerd, container probes, httpd, long watch
#            while the host reloads, breaks and kills the broker
#   cached - cached start: watch from the first second of userspace
J=/mnt/jlp
mkdir -p /mnt/fx/rw /mnt/fx/ro $J /mnt/dstate
mount -t virtiofs fx-rw /mnt/fx/rw
mount -t virtiofs -o ro fx-ro /mnt/fx/ro
mount -t virtiofs jlp $J
mount -t virtiofs dstate /mnt/dstate
PHASE=$(cat $J/phase)
O=$J/out-$PHASE
mkdir -p $O
exec >$O/init.log 2>&1
set -x
ts() { date +%s; }
mark() { echo "$(ts) $*" >> $O/marks.txt; }
mark boot
ip link set eth0 up

if [ "$PHASE" = cached ]; then
  # Watch starts before DHCP: nothing may be reachable before or after the lease.
  $J/vmprobe-linux watch -plan $J/plan.json -context guest-root -interval 500ms -duration 40s -timeout 2s \
    -out $O/r-watch.json -timeline $O/watch.jsonl &
  W=$!
  t0=$(ts); udhcpc -i eth0 -n -q -t 10; echo "udhcpc rc=$? $(( $(ts) - t0 ))s" >> $O/marks.txt
  mark net
  wait $W
  mark watch
  touch $O/DONE
  while :; do sleep 3600; done
fi

t0=$(ts); udhcpc -i eth0 -n -q -t 10; echo "udhcpc rc=$? $(( $(ts) - t0 ))s" >> $O/marks.txt
ip addr; ip route; ip -6 addr; ip -6 route; cat /etc/resolv.conf; ls /sys/class/net
mark net
command -v nft || apk add --no-cache nftables; echo "apk rc=$?" >> $O/marks.txt
mark apk

$J/vmprobe-linux guest -plan $J/plan.json -context guest-root -out $O/r-guest-root.json | tail -3
mark probe-root
mkdir -p /tmp/u && chmod 777 /tmp/u
su nobody -s /bin/sh -c "$J/vmprobe-linux guest -plan $J/plan.json -context guest-user -out /tmp/u/r-guest-user.json" | tail -3
cp /tmp/u/r-guest-user.json $O/
mark probe-user

if [ ! -f /mnt/dstate/docker.img ]; then
  truncate -s 20G /mnt/dstate/docker.img && mkfs.ext4 -q -F /mnt/dstate/docker.img
fi
mkdir -p /var/lib/docker
mount -o loop /mnt/dstate/docker.img /var/lib/docker
sysctl -w net.ipv4.ip_forward=1
dockerd --data-root /var/lib/docker --firewall-backend=nftables > $O/dockerd.log 2>&1 &
for i in $(seq 1 40); do docker info >/dev/null 2>&1 && break; sleep 1; done
mark dockerd
docker run --rm -v $J:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-bridge -out /p/out-full/r-container-bridge.json | tail -3
docker run --rm --privileged --network=host -v $J:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-hostnet-privileged -out /p/out-full/r-container-hostnet-privileged.json | tail -3
mark probe-containers

(cd /tmp && busybox httpd -f -p 0.0.0.0:4096) &
mark httpd
# The host reloads a broken policy, restores it, then kills and restarts the
# broker while this watch runs.
touch $O/WATCH
$J/vmprobe-linux watch -plan $J/plan.json -context guest-root -interval 1s -duration 120s -timeout 2s \
  -out $O/r-watch.json -timeline $O/watch.jsonl
mark watch
ip addr show eth0; ip route
touch $O/DONE
while :; do sleep 3600; done
