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
# Keeps dockerd's nftables package and the resize tool, with their
# dependencies, so later boots do not need the network.
APKCACHE=$DATA_DIR/.jailoc-apk-cache
# Disposable APK cache on the root filesystem: packages that are downloaded
# before the data disk is mounted or grown land here, and move to APKCACHE
# once the file system has room.
APKSTAGE=/tmp/jailoc-apk-stage
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
# only), else from the network into the root filesystem and its staging cache.
install_resize_tool() {
	command -v resize2fs >/dev/null 2>&1 && return 0
	if [ -d "$APKCACHE" ] && apk add -q --no-network --cache-dir "$APKCACHE" e2fsprogs-extra 2>/dev/null; then
		return 0
	fi
	mkdir -p "$APKSTAGE" || fail "create apk stage failed; cannot grow the data disk"
	apk add -q --cache-dir "$APKSTAGE" e2fsprogs-extra || fail "installing resize2fs (e2fsprogs-extra) failed; cannot grow the data disk"
}

# superblock_word prints the little-endian 32-bit field at an absolute byte
# offset of the ext4 superblock (which starts at byte 1024 of the device).
superblock_word() {
	word=$(od -An -t u4 -j "$1" -N 4 "$DATA_DEV") || return 1
	set -- $word
	[ $# -eq 1 ] || return 1
	echo "$1"
}

# data_fs_bytes prints the size of the ext4 file system on the data disk in
# bytes: blocks_lo at 1028 (plus blocks_hi at 1360 when the 64bit incompat
# feature, bit 0x80 of the flags at 1120, is set) times the block size,
# 1024 << log_block_size at 1048.
data_fs_bytes() {
	blocks=$(superblock_word 1028) || return 1
	log_bs=$(superblock_word 1048) || return 1
	incompat=$(superblock_word 1120) || return 1
	[ "$log_bs" -le 6 ] || return 1
	if [ $((incompat & 128)) -ne 0 ]; then
		blocks_hi=$(superblock_word 1360) || return 1
		blocks=$((blocks_hi * 4294967296 + blocks))
	fi
	echo $((blocks * (1024 << log_bs)))
}

# grow_data_disk grows the file system to the disk when docker_disk or
# builder_disk was raised since the file system was made. The file system size
# decides, not a record of earlier boots, so a growth that did not happen is
# retried. The format leaves 1 MiB of the disk unformatted: that much is no
# growth. It runs before anything is installed onto the data disk: a full disk
# would refuse the packages.
grow_data_disk() {
	fs_bytes=$(data_fs_bytes) || fail "reading the file system size of $DATA_DEV failed; cannot tell whether the data disk must grow"
	if [ $((DISK_SIZE - fs_bytes)) -le 1048576 ]; then
		return 0
	fi
	install_resize_tool
	resize2fs "$DATA_DEV" || fail "resize2fs $DATA_DEV failed"
}

# cache_packages moves the packages staged on the root filesystem (the format
# and resize tools with their dependencies) to the package cache on the data
# disk, then installs nftables, which dockerd's nftables backend needs, into
# that cache for later boots.
cache_packages() {
	mkdir -p "$APKCACHE" || log "create apk cache failed"
	if [ -d "$APKSTAGE" ]; then
		cp -a "$APKSTAGE"/. "$APKCACHE"/ || log "caching staged packages failed"
	fi
	apk add -q --no-network --cache-dir "$APKCACHE" nftables 2>/dev/null ||
		apk add -q --cache-dir "$APKCACHE" nftables || log "apk add nftables failed"
}

# prepare_data_disk formats the data disk on first use, mounts it, grows the
# file system and only then installs packages onto it.
prepare_data_disk() {
	fresh=
	mkdir -p "$DATA_DIR"
	if [ -z "$(blkid "$DATA_DEV")" ]; then
		# The resize tool comes along so that later boots find it, with the
		# dependencies of both, in the package cache.
		mkdir -p "$APKSTAGE" || fail "create apk stage failed"
		apk add -q --cache-dir "$APKSTAGE" e2fsprogs e2fsprogs-extra || fail "apk add e2fsprogs failed"
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
