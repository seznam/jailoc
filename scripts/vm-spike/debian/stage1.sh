#!/bin/sh
# Stage 1, PID 1 in a stock debian:trixie rootfs: configure the network
# statically with Alpine's busybox (the image has no network tools), install
# systemd, Docker and friends, and prepare the units for systemd boots.
set -x
J=/mnt/jlp
mkdir -p $J
mount -t virtiofs jlp $J
exec > $J/out/stage1.log 2>&1
NET=$(cat $J/net)
BB="$J/ld-musl-aarch64.so.1 $J/busybox"
$BB ip link set eth0 up
$BB ip addr add "$NET.2/24" dev eth0
$BB ip route add default via "$NET.1"
rm -f /etc/resolv.conf
echo "nameserver $NET.1" > /etc/resolv.conf

export DEBIAN_FRONTEND=noninteractive
t0=$(date +%s)
apt-get update -q || exit 1
apt-get install -y -q --no-install-recommends \
  systemd systemd-sysv systemd-resolved udev dbus iproute2 ca-certificates curl \
  nftables e2fsprogs procps util-linux || exit 1
# Docker CE: the libkrun kernel lacks the xtables NAT targets Debian's docker.io
# needs, so dockerd runs with the nftables firewall backend (Docker >= 28).
install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc || exit 1
echo "deb [signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian trixie stable" > /etc/apt/sources.list.d/docker.list
apt-get update -q || exit 1
apt-get install -y -q --no-install-recommends docker-ce docker-ce-cli containerd.io || exit 1
mkdir -p /etc/docker
echo '{"firewall-backend":"nftables"}' > /etc/docker/daemon.json
echo net.ipv4.ip_forward=1 > /etc/sysctl.d/90-docker-forward.conf
echo "apt seconds $(( $(date +%s) - t0 ))"

# Network: DHCP from the broker on the only NIC.
mkdir -p /etc/systemd/network
printf '[Match]\nName=eth0\n\n[Network]\nDHCP=ipv4\nIPv6AcceptRA=no\nLinkLocalAddressing=no\n' > /etc/systemd/network/10-eth0.network
systemctl enable systemd-networkd systemd-resolved docker
# The libkrun kernel has no loadable modules.
systemctl mask systemd-modules-load.service
ln -sf /run/systemd/resolve/stub-resolv.conf /etc/resolv.conf

# Docker state on the virtio-blk disk.
[ -n "$(blkid /dev/vda)" ] || mkfs.ext4 -q -F -E nodiscard /dev/vda
blkid /dev/vda
mkdir -p /tmp/vdatest && mount /dev/vda /tmp/vdatest && echo stage1-mount-ok && umount /tmp/vdatest
mkdir -p /var/lib/docker
cat >> /etc/fstab <<'EOF'
jlp /mnt/jlp virtiofs defaults,nofail 0 0
/dev/vda /var/lib/docker ext4 defaults,nofail 0 0
EOF

# Verification unit: runs once the system is up, writes to the share, powers off.
cp $J/verify.sh /usr/local/bin/jlp-verify
chmod 755 /usr/local/bin/jlp-verify
cat > /etc/systemd/system/jlp-verify.service <<'EOF'
[Unit]
Description=jailoc VM spike verification
Wants=network-online.target docker.service
After=network-online.target docker.service mnt-jlp.mount

[Service]
Type=oneshot
ExecStart=/usr/local/bin/jlp-verify

[Install]
WantedBy=multi-user.target
EOF
systemctl enable jlp-verify systemd-networkd-wait-online
echo root:root | chpasswd
touch $J/out/STAGE1-DONE
sync
