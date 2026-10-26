#!/bin/sh
# Guest init for the go-microvm P1 spike. Phase is read from /mnt/jlp/phase:
#   full    - probes, dockerd, image build, Testcontainers, dind, container probes, watch
#   persist - restart check: dockerd on the same disk image, image and volume survive
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
uname -a; cat /proc/cmdline; mount; ip addr; ip route; ip -6 route; cat /etc/resolv.conf
ls /sys/class/net; ls -l /sys/class/net/*/device 2>&1
ls /proc/config.gz /lib/modules 2>&1 > $O/kconfig.txt; (zcat /proc/config.gz 2>/dev/null || echo no-proc-config) | grep -E 'NF_NAT|MASQ|NFT_(NAT|MASQ|CT)|NF_TABLES=|IP_NF_(NAT|FILTER|TARGET)|XT_(MATCH_ADDRTYPE|MATCH_CONNTRACK)|BRIDGE=|BRIDGE_NETFILTER|VETH|OVERLAY_FS|BLK_DEV_LOOP|EXT4_FS=|IPV6=|VSOCK' >> $O/kconfig.txt
# libkrun leaves eth0 down when the image entrypoint is overridden; configure it via DHCP from the in-process network.
ip link set eth0 up
udhcpc -i eth0 -n -q -t 5; echo "udhcpc rc=$?"
ip addr show eth0; ip route; cat /etc/resolv.conf
mark net
command -v nft findmnt mkfs.ext4 || apk add --no-cache e2fsprogs nftables findmnt; echo "apk rc=$?"
mark apk

# Persistent Docker state: ext4 image file on the dstate share, loop-mounted.
if [ ! -f /mnt/dstate/docker.img ]; then
  truncate -s 30G /mnt/dstate/docker.img && mkfs.ext4 -q -F /mnt/dstate/docker.img
fi
mkdir -p /var/lib/docker
mount -o loop /mnt/dstate/docker.img /var/lib/docker; echo "loop-mount rc=$?"
findmnt -T /var/lib/docker
mark disk

start_dockerd() {
  sysctl -w net.ipv4.ip_forward=1
  for args in "--firewall-backend=nftables" "" "--iptables=false --ip6tables=false"; do
    echo "== dockerd $args" >> $O/dockerd.log
    # shellcheck disable=SC2086
    dockerd --data-root /var/lib/docker $args >> $O/dockerd.log 2>&1 &
    DPID=$!
    for i in $(seq 1 40); do
      if docker info >/dev/null 2>&1; then echo "dockerd-up args=[$args] after ${i}s" >> $O/marks.txt; return 0; fi
      kill -0 $DPID 2>/dev/null || break
      sleep 1
    done
    kill $DPID 2>/dev/null; wait $DPID 2>/dev/null
  done
  return 1
}

if [ "$PHASE" = dind ]; then
  start_dockerd; mark dockerd
  for extra in "" "--firewall-backend=nftables"; do
    id=$(docker run -d --privileged docker:dind dockerd-entrypoint.sh $extra)
    ok=no
    for i in $(seq 1 40); do docker exec $id docker info >/dev/null 2>&1 && { ok=yes; break; }; docker inspect -f '{{.State.Running}}' $id | grep -q true || break; sleep 1; done
    echo "dind extra=[$extra] daemon=$ok" >> $O/marks.txt
    [ $ok = yes ] && { docker exec $id docker run --rm alpine echo nested-ok >> $O/marks.txt 2>&1; echo "nested rc=$?" >> $O/marks.txt; }
    docker logs $id 2>&1 | grep -E 'failed to start|level=(error|fatal)' | tail -3 >> $O/marks.txt
    docker rm -f $id >/dev/null
  done
  touch $O/DONE
  while :; do sleep 3600; done
fi

if [ "$PHASE" = persist ]; then
  start_dockerd; mark dockerd
  docker image ls jailoc-base:spike --format 'image-survived {{.ID}}'
  docker run --rm -v jlp-marker:/m alpine cat /m/marker
  mark persist-checked
  touch $O/DONE
  while :; do sleep 3600; done
fi

# Network/DNS/iface/vsock/fs probes as guest root and as an unprivileged user.
$J/vmprobe-linux guest -plan $J/plan.json -context guest-root -out $O/r-guest-root.json | tail -3
mark probe-root
mkdir -p /tmp/u && chmod 777 /tmp/u
su nobody -s /bin/sh -c "$J/vmprobe-linux guest -plan $J/plan.json -context guest-user -out /tmp/u/r-guest-user.json" | tail -3
cp /tmp/u/r-guest-user.json $O/
mark probe-user

start_dockerd; mark dockerd
docker info --format 'driver={{.Driver}} version={{.ServerVersion}} root={{.DockerRootDir}}'

# Step 4: build the jailoc base image from the embedded assets inside the guest.
t0=$(ts)
(cd $J/assets && docker build -t jailoc-base:spike .) > $O/build.log 2>&1
echo "build rc=$? $(( $(ts) - t0 ))s" >> $O/marks.txt; tail -5 $O/build.log
docker image ls jailoc-base:spike --format '{{.Repository}}:{{.Tag}} {{.Size}}'

# Step 5: privileged Testcontainers workload and nested dind.
t0=$(ts)
docker run --rm --privileged -v /var/run/docker.sock:/var/run/docker.sock -v $J/tc:/tc -w /tc -e GOFLAGS=-mod=mod golang:1.26 \
  sh -c 'go mod tidy >/dev/null 2>&1; go test -count=1 -v ./...' > $O/tc.log 2>&1
echo "testcontainers rc=$? $(( $(ts) - t0 ))s" >> $O/marks.txt; tail -15 $O/tc.log
id=$(docker run -d --privileged docker:dind)
for i in $(seq 1 40); do docker exec $id docker info --format 'dind-ok {{.ServerVersion}} {{.Driver}}' 2>/dev/null && break; sleep 1; done
docker exec $id docker run --rm alpine echo nested-ok; echo "dind rc=$?" >> $O/marks.txt
docker rm -f $id >/dev/null
docker volume create jlp-marker >/dev/null && docker run --rm -v jlp-marker:/m alpine sh -c "echo persisted-$(date +%s) > /m/marker && cat /m/marker"

# Container contexts.
docker run --rm -v $J:/p alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-bridge -out /p/out-full/r-container-bridge.json | tail -3
docker run --rm --privileged --network=host -v $J:/p -v /mnt/fx:/mnt/fx alpine /p/vmprobe-linux guest -plan /p/plan.json -context container-hostnet-privileged -out /p/out-full/r-container-hostnet-privileged.json | tail -3
mark probe-containers

# Fail-closed watch (the broker lives in the runner process; killing it kills the VM).
$J/vmprobe-linux watch -plan $J/plan.json -context guest-root -duration 30s -out $O/r-watch.json -timeline $O/watch.jsonl
mark watch

# Ingress target for the host-side check.
(cd /tmp && busybox httpd -f -p 0.0.0.0:4096 || python3 -m http.server 4096) &
mark httpd
touch $O/DONE
while :; do sleep 3600; done
