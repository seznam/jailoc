#!/bin/sh
# jailoc vm backend guest init (Alpine docker:dind image, run by libkrun init).
# Mounts the host shares listed in /jailoc-shares at their host paths, takes
# the broker's DHCP lease, keeps Docker state on the virtio-blk data disk and
# runs the guest dockerd for the host-side jailoc (via the broker) and for the
# workspace container (via the unix socket).
set -u
GATEWAY=@GATEWAY@
GUEST=@GUEST@
DOCKER_PORT=@DOCKER_PORT@
AGENT_GID=@AGENT_GID@
CONTROL=@CONTROL@

log() { echo "jailoc-init: $*"; }

while IFS='	' read -r tag mode path; do
	[ -n "$tag" ] || continue
	mkdir -p "$path"
	if [ "$mode" = ro ]; then
		mount -t virtiofs -o ro "$tag" "$path" || log "mount $tag failed"
	else
		mount -t virtiofs "$tag" "$path" || log "mount $tag failed"
	fi
done < /jailoc-shares

ip link set lo up
ip link set eth0 up
until udhcpc -i eth0 -n -q -t 10; do log "dhcp failed, retrying"; sleep 1; done

mkdir -p /var/lib/docker
if [ -z "$(blkid /dev/vda)" ]; then
	apk add -q --no-cache e2fsprogs || log "apk add e2fsprogs failed"
	mkfs.ext4 -q -F /dev/vda || log "mkfs failed"
fi
mount -t ext4 /dev/vda /var/lib/docker || log "mount data disk failed"

# dockerd's nftables backend needs the nft binary; keep the package cache on
# the data disk so later boots do not need the network for it.
APKCACHE=/var/lib/docker/.jailoc-apk-cache
mkdir -p "$APKCACHE"
apk add -q --no-network --cache-dir "$APKCACHE" nftables 2>/dev/null ||
	apk add -q --cache-dir "$APKCACHE" nftables || log "apk add nftables failed"

sysctl -q -w net.ipv4.ip_forward=1

# Only the broker gateway may use the TCP API; containers reach dockerd
# through the unix socket the workspace mounts.
nft add table inet jailoc
nft add chain inet jailoc input '{ type filter hook input priority -10; policy accept; }'
nft add rule inet jailoc input tcp dport "$DOCKER_PORT" ip saddr != "$GATEWAY" drop

# Pid files and sockets in the root filesystem are never live at boot.
rm -f /var/run/docker.pid /var/run/docker.sock /var/run/docker/containerd/containerd.pid

# Trust the workspace's forwarded CA bundle for image pulls during builds.
if [ -s /jailoc-ca.pem ]; then
  cat /jailoc-ca.pem >> /etc/ssl/certs/ca-certificates.crt
  log "CA bundle installed"
fi
dockerd --data-root /var/lib/docker --firewall-backend=nftables \
	-H unix:///var/run/docker.sock -G "$AGENT_GID" \
	-H "tcp://$GUEST:$DOCKER_PORT" --tls=false &
pid=$!

# The supervisor requests a clean stop by creating $CONTROL/shutdown: stop
# dockerd, flush and unmount the data disk, then power off.
while kill -0 "$pid" 2>/dev/null; do
	if [ -e "$CONTROL/shutdown" ]; then
		log "shutdown requested"
		kill -TERM "$pid"
		i=0
		while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 150 ]; do
			sleep 0.1
			i=$((i + 1))
		done
		break
	fi
	sleep 0.5
done
log "dockerd stopped"
sync
umount /var/lib/docker 2>/dev/null || log "unmount data disk failed"
poweroff -f
