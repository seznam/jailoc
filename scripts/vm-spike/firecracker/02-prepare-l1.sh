#!/usr/bin/env bash
set -euo pipefail

base=/var/lib/jlp-fc
repo=/path/to/jailoc-firecracker
fix="$base/fixture"
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nfs-kernel-server nfs-common docker.io socat jq

sudo mkdir -p "$base/root/root/.ssh" "$base/root/mnt/fx/ro" "$base/root/mnt/fx/rw" "$base/root/var/lib/docker" "$fix"
if [ ! -f "$base/guest-key" ]; then
  ssh-keygen -q -t ed25519 -N '' -f "$base/guest-key"
fi
sudo cp "$base/guest-key.pub" "$base/root/root/.ssh/authorized_keys"
sudo chmod 700 "$base/root/root/.ssh"
sudo chmod 600 "$base/root/root/.ssh/authorized_keys"
sudo sed -i 's/^#\?PermitRootLogin .*/PermitRootLogin prohibit-password/' "$base/root/etc/ssh/sshd_config"
sudo tee "$base/root/etc/systemd/system/jlp-fc-mounts.service" >/dev/null <<'EOF'
[Unit]
Description=Mount workspace disks and NFS export
After=fcnet.service
Before=docker.service
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/bin/bash -c 'mountpoint -q /var/lib/docker || mount /dev/vdb /var/lib/docker; mountpoint -q /mnt/fx/ro || mount -o ro /dev/vdc /mnt/fx/ro; ip route replace default via 172.16.0.1 dev eth0; mountpoint -q /mnt/fx/rw || mount -t nfs -o vers=4 172.16.0.1:/ /mnt/fx/rw'
[Install]
WantedBy=multi-user.target
EOF
sudo ln -sf /etc/systemd/system/jlp-fc-mounts.service "$base/root/etc/systemd/system/multi-user.target.wants/jlp-fc-mounts.service"
sudo tee "$base/root/etc/resolv.conf" >/dev/null <<'EOF'
nameserver 1.1.1.1
options single-request-reopen
EOF
sudo truncate -s 3G "$base/root.ext4"
sudo mkfs.ext4 -q -F -d "$base/root" "$base/root.ext4"
sudo truncate -s 12G "$base/docker.ext4"
sudo mkfs.ext4 -q -F "$base/docker.ext4"
sudo mkdir -p "$fix/rw" "$fix/ro" "$fix/outside"
sudo truncate -s 512M "$base/ro.ext4"
sudo mkfs.ext4 -q -F -d "$fix/ro" "$base/ro.ext4"
sudo mkdir -p /srv/jlp-fc-export
if ! mountpoint -q /srv/jlp-fc-export; then
  sudo mount --bind "$fix/rw" /srv/jlp-fc-export
fi
sudo mkdir -p /etc/exports.d
sudo tee /etc/exports.d/jlp-fc.exports >/dev/null <<'EOF'
/srv/jlp-fc-export 172.16.0.2/32(rw,sync,no_subtree_check,root_squash,fsid=0)
EOF
sudo exportfs -ra
sudo systemctl restart nfs-kernel-server
sudo ip tuntap add dev tap-fc mode tap
sudo ip addr add 172.16.0.1/30 dev tap-fc
sudo ip link set tap-fc up
sudo sysctl -w net.ipv4.ip_forward=1
sudo nft add table ip jlp_fc
sudo nft 'add chain ip jlp_fc postrouting { type nat hook postrouting priority srcnat; policy accept; }'
sudo nft add rule ip jlp_fc postrouting ip saddr 172.16.0.2 oifname eth0 masquerade
sudo systemctl disable --now docker.service docker.socket || true
printf 'Prepared root, data, ro disks, NFS export, tap-fc, nft NAT\n'
sudo ls -lh "$base"/*.ext4
sudo exportfs -v
sudo nft list table ip jlp_fc
