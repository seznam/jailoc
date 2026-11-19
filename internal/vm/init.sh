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
# Size of the data disk in bytes; the build cache GC limits derive from it.
DISK_SIZE=@DISK_SIZE@

DATA_DEV=/dev/vda
DATA_DIR=/var/lib/docker
# Keeps dockerd's nftables package so later boots do not need the network.
APKCACHE=$DATA_DIR/.jailoc-apk-cache
RUN_DIR=/run
DAEMON_CONFIG=$RUN_DIR/jailoc-dockerd.json
# Tenths of a second dockerd gets to stop after SIGTERM.
STOP_TICKS=150

# jailoc-init:functions begin
log() { echo "jailoc-init: $*"; }

# Docker state must persist on the data disk; never fall back to the
# disposable root filesystem.
fail() { log "$*"; poweroff -f; exit 1; }

# log_usage prints the block and inode usage of the data disk: either can run
# out first, and dockerd's own errors do not say which.
log_usage() {
	log "data disk usage ($1):"
	df -k "$DATA_DIR" 2>&1 | sed 's/^/jailoc-init:   /'
	df -i "$DATA_DIR" 2>&1 | sed 's/^/jailoc-init:   /'
}

# install_resize_tool makes resize2fs available without writing to the data
# disk, which may be full: from the package cache on the data disk (read
# only), else from the network straight into the root filesystem.
install_resize_tool() {
	command -v resize2fs >/dev/null 2>&1 && return 0
	if [ -d "$APKCACHE" ] && apk add -q --no-network --cache-dir "$APKCACHE" e2fsprogs-extra 2>/dev/null; then
		return 0
	fi
	apk add -q --no-cache e2fsprogs-extra || fail "installing resize2fs (e2fsprogs-extra) failed; cannot grow the data disk"
	RESIZE_TOOL_FETCHED=1
}

# grow_data_disk grows the file system to the disk when docker_disk or
# builder_disk was raised since the last boot. It runs before anything is
# installed onto the data disk: a full disk would refuse the packages.
grow_data_disk() {
	install_resize_tool
	resize2fs "$DATA_DEV" || fail "resize2fs $DATA_DEV failed"
}

# cache_packages installs nftables, which dockerd's nftables backend needs,
# keeping the package cache on the data disk for later boots.
cache_packages() {
	mkdir -p "$APKCACHE" || log "create apk cache failed"
	apk add -q --no-network --cache-dir "$APKCACHE" nftables 2>/dev/null ||
		apk add -q --cache-dir "$APKCACHE" nftables || log "apk add nftables failed"
	if [ -n "$RESIZE_TOOL_FETCHED" ]; then
		apk fix -q --reinstall --cache-dir "$APKCACHE" e2fsprogs-extra || log "caching e2fsprogs-extra failed"
	fi
}

# prepare_data_disk formats the data disk on first use, mounts it, grows the
# file system and only then installs packages onto it.
prepare_data_disk() {
	RESIZE_TOOL_FETCHED=
	fresh=
	mkdir -p "$DATA_DIR"
	if [ -z "$(blkid "$DATA_DEV")" ]; then
		apk add -q --no-cache e2fsprogs || fail "apk add e2fsprogs failed"
		# mke2fs always zeroes the last 64 KiB of the file system, and libkrun's
		# imago < 0.2.5 truncates the disk image when that reaches its end
		# (libkrun#949): leave 1 MiB (256 blocks) unformatted.
		mkfs.ext4 -q -F -b 4096 -E nodiscard "$DATA_DEV" "$((DISK_SIZE / 4096 - 256))" || fail "mkfs failed"
		fresh=1
	fi
	mount -t ext4 "$DATA_DEV" "$DATA_DIR" || fail "mount data disk failed"
	log_usage "after mount"
	if [ -z "$fresh" ]; then
		grow_data_disk
		log_usage "after growth"
	fi
	cache_packages
}

# write_daemon_config sets dockerd's build cache GC targets: at most half the
# data disk used by the cache and a fifth of the disk kept free. They are GC
# targets, not hard quotas. The values are absolute byte counts, which is what
# dockerd's builder.gc accepts. The reserved floor is an explicit 0 so that it
# cannot raise the targets (dockerd 29 sets a reserved floor only when asked,
# and fills in percentage defaults only when all three values are unset). The
# config lives on tmpfs.
write_daemon_config() {
	max_used=$((DISK_SIZE / 2))
	min_free=$((DISK_SIZE / 5))
	cat > "$DAEMON_CONFIG" <<CONFIG || fail "write dockerd config failed"
{
  "builder": {
    "gc": {
      "enabled": true,
      "defaultReservedSpace": "0",
      "defaultMaxUsedSpace": "$max_used",
      "defaultMinFreeSpace": "$min_free"
    }
  }
}
CONFIG
	log "dockerd build cache GC targets: cache at most $max_used bytes, disk at least $min_free bytes free"
}

# run_dockerd runs dockerd until it exits and logs why. The supervisor
# requests a clean stop by creating $CONTROL/shutdown: dockerd then gets
# SIGTERM, and SIGKILL after STOP_TICKS tenths of a second.
run_dockerd() {
	dockerd --config-file "$DAEMON_CONFIG" --data-root "$DATA_DIR" --firewall-backend=nftables \
		-H unix:///var/run/docker.sock -G "$AGENT_GID" \
		-H "tcp://$GUEST:$DOCKER_PORT" --tls=false &
	pid=$!
	(
		while [ ! -e "$CONTROL/shutdown" ]; do sleep 0.1; done
		log "shutdown requested"
		kill -TERM "$pid" 2>/dev/null
		i=0
		while kill -0 "$pid" 2>/dev/null && [ "$i" -lt "$STOP_TICKS" ]; do
			sleep 0.1
			i=$((i + 1))
		done
		if kill -0 "$pid" 2>/dev/null; then
			log "dockerd ignored SIGTERM for $STOP_TICKS ticks; killing it"
			kill -KILL "$pid" 2>/dev/null
		fi
	) &
	watcher=$!
	status=0
	wait "$pid" || status=$?
	kill "$watcher" 2>/dev/null
	wait "$watcher" 2>/dev/null
	if [ -e "$CONTROL/shutdown" ]; then
		log "dockerd stopped after shutdown request (exit status $status)"
	else
		log "dockerd exited unexpectedly with status $status"
		log_usage "after dockerd exit"
	fi
}
# jailoc-init:functions end

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
# Match the broker's MTU; large frames cut the broker's per-packet work.
ip link set eth0 mtu @MTU@
until udhcpc -i eth0 -n -q -t 10; do log "dhcp failed, retrying"; sleep 1; done

# Trust the workspace's forwarded CA bundle before any download (apk, image
# pulls during builds).
if [ -s /jailoc-ca.pem ]; then
	cat /jailoc-ca.pem >> /etc/ssl/certs/ca-certificates.crt
	log "CA bundle installed"
fi

prepare_data_disk

sysctl -q -w net.ipv4.ip_forward=1

# Only the broker gateway may use the TCP API; containers reach dockerd
# through the unix socket the workspace mounts.
nft add table inet jailoc
nft add chain inet jailoc input '{ type filter hook input priority -10; policy accept; }'
nft add rule inet jailoc input tcp dport "$DOCKER_PORT" ip saddr != "$GATEWAY" drop

# Runtime state lives on tmpfs: the virtio-fs root filesystem belongs to the
# host user on Linux hosts, so dockerd cannot chown its socket there. It
# also leaves no stale pid files or sockets from an earlier boot.
mount -t tmpfs -o mode=755,nosuid,nodev tmpfs "$RUN_DIR" || fail "mount /run failed"

write_daemon_config
run_dockerd
log "dockerd stopped"
sync
umount "$DATA_DIR" 2>/dev/null || log "unmount data disk failed"
poweroff -f
