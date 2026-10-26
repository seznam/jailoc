#!/usr/bin/env bash
# Firecracker spike helper. Runs INSIDE the L1 Lima VM (jlp-fc-host), which plays the
# Linux host. Usage: fc.sh prepare | boot | stop | ssh CMD | policy on|off|reload
set -euo pipefail
CL=/path/to/jailoc-firecracker
D=$CL/scripts/vm-spike/firecracker
W=/var/lib/jlp-fc
FX=$W/fx
FC=$W/release-v1.17.0-aarch64/firecracker-v1.17.0-aarch64
KEY=$W/guest-key
GUEST=172.16.0.2
SSHO=(-i "$KEY" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=30)

gssh() { sudo ssh "${SSHO[@]}" root@$GUEST "$@"; }

prepare() {
  # L1 host networking: docker.io in L1 left a FORWARD policy drop table; remove it.
  sudo systemctl disable --now docker.service docker.socket 2>/dev/null || true
  sudo nft delete table ip filter 2>/dev/null || true
  sudo nft delete table ip6 filter 2>/dev/null || true
  sudo sysctl -qw net.ipv4.ip_forward=1
  ip link show tap-fc >/dev/null 2>&1 || {
    sudo ip tuntap add dev tap-fc mode tap
    sudo ip addr add 172.16.0.1/30 dev tap-fc
    sudo ip link set tap-fc up
  }
  sudo nft list table ip jlp_fc >/dev/null 2>&1 || {
    sudo nft add table ip jlp_fc
    sudo nft 'add chain ip jlp_fc postrouting { type nat hook postrouting priority srcnat; policy accept; }'
    sudo nft add rule ip jlp_fc postrouting ip saddr 172.16.0.2 oifname eth0 masquerade
  }
  # Fixtures + plan (L1 is the host; the Mac en0 and the Mac loopback alias are LAN sentinels).
  sudo rm -r "$FX" 2>/dev/null || true
  sudo install -d -o lima -g lima "$FX"
  mkdir -p "$D/results"
  "$CL/bin/vmprobe-linux" plan -candidate firecracker -dir "$FX" -guest-prefix /mnt/fx \
    -port 47841 -alt-port 47842 -ipv6 disabled \
    -lan-sentinel 10.0.0.73 -lan-sentinel 192.168.5.2 -out "$D/results/plan.json"
  # Read-write share: NFSv4 export from L1, all_squash to the host user (lima 502:1000).
  sudo install -d /etc/exports.d
  echo "$FX/rw 172.16.0.2/32(rw,sync,no_subtree_check,fsid=0,all_squash,anonuid=502,anongid=1000)" |
    sudo tee /etc/exports.d/jlp-fc.exports >/dev/null
  sudo rm -f /etc/exports.d/jlp-fc-old.exports
  sudo exportfs -ra
  sudo systemctl restart nfs-server
  # Read-only share: ext4 image of the ro fixture attached as a read-only drive.
  sudo rm -f "$W/ro.ext4"
  sudo truncate -s 64M "$W/ro.ext4"
  sudo mkfs.ext4 -q -F -d "$FX/ro" "$W/ro.ext4"
  # Root fs from the Firecracker CI Ubuntu 24.04 squashfs.
  sudo rm -r "$W/rootsrc" 2>/dev/null || true
  sudo unsquashfs -q -d "$W/rootsrc" "$W/ubuntu-24.04.squashfs"
  R=$W/rootsrc
  sudo install -d -m 700 "$R/root/.ssh"
  sudo install -m 600 "$KEY.pub" "$R/root/.ssh/authorized_keys"
  sudo install -d "$R/mnt/fx/rw" "$R/mnt/fx/ro" "$R/var/lib/docker" "$R/opt/jlp"
  printf 'nameserver 1.1.1.1\n' | sudo tee "$R/etc/resolv.conf" >/dev/null
  sudo tee -a "$R/etc/fstab" >/dev/null <<'EOF'
/dev/vdb /var/lib/docker ext4 defaults,nofail 0 2
/var/lib/docker/containerd /var/lib/containerd none bind,x-systemd.requires-mounts-for=/var/lib/docker,nofail 0 0
/dev/vdc /mnt/fx/ro ext4 ro,noload,nofail 0 0
172.16.0.1:/ /mnt/fx/rw nfs4 vers=4.2,addr=172.16.0.1,clientaddr=172.16.0.2,_netdev,nofail 0 0
EOF
  sudo chroot "$R" useradd -m -u 1000 -s /bin/bash agent 2>/dev/null || true
  # apt pulls in systemd-resolved later, which replaces resolv.conf with a stub that has no uplink.
  sudo install -d "$R/etc/systemd/resolved.conf.d"
  printf '[Resolve]\nDNS=1.1.1.1\n' | sudo tee "$R/etc/systemd/resolved.conf.d/jlp.conf" >/dev/null
  # The CI rootfs has no mount.nfs helper and no gpgv for apt; copy the L1 (same Ubuntu 24.04 arm64) binary.
  sudo chmod 1777 "$R/tmp"
  sudo install -m 4755 /sbin/mount.nfs "$R/sbin/mount.nfs"
  sudo ln -sf mount.nfs "$R/sbin/mount.nfs4"
  # fcnet only assigns the address; add the default route via the TAP host end.
  sudo tee "$R/etc/systemd/system/jlp-route.service" >/dev/null <<'EOF'
[Unit]
After=fcnet.service
Before=network-online.target remote-fs-pre.target
Wants=network-online.target
[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=/sbin/ip route replace default via 172.16.0.1 dev eth0
[Install]
WantedBy=multi-user.target
EOF
  sudo ln -sf /etc/systemd/system/jlp-route.service "$R/etc/systemd/system/multi-user.target.wants/jlp-route.service"
  sudo rm -f "$W/root.ext4"
  sudo truncate -s 4G "$W/root.ext4"
  sudo mkfs.ext4 -q -F -d "$R" "$W/root.ext4"
  sudo rm -f "$W/docker.ext4"
  sudo truncate -s 16G "$W/docker.ext4"
  sudo mkfs.ext4 -q -F "$W/docker.ext4"
  cat > "$W/vm.json" <<EOF
{
  "boot-source": {"kernel_image_path": "$W/vmlinux-6.18.54",
    "boot_args": "console=ttyS0 reboot=k panic=1 root=/dev/vda rw"},
  "drives": [
    {"drive_id": "rootfs", "path_on_host": "$W/root.ext4", "is_root_device": true, "is_read_only": false},
    {"drive_id": "docker", "path_on_host": "$W/docker.ext4", "is_root_device": false, "is_read_only": false},
    {"drive_id": "fxro", "path_on_host": "$W/ro.ext4", "is_root_device": false, "is_read_only": true}
  ],
  "network-interfaces": [{"iface_id": "net1", "guest_mac": "06:00:ac:10:00:02", "host_dev_name": "tap-fc"}],
  "machine-config": {"vcpu_count": 4, "mem_size_mib": 4096}
}
EOF
  echo "prepared"
}

boot() {
  pgrep -f "$FC --api-sock" >/dev/null && { echo "already running"; return; }
  sudo rm -f "$W/api.sock"
  t0=$(date +%s.%N)
  sudo setsid -f "$FC" --api-sock "$W/api.sock" --config-file "$W/vm.json" </dev/null >"$W/console.log" 2>&1
  for _ in $(seq 1 300); do gssh true 2>/dev/null && break; sleep 0.2; done
  t1=$(date +%s.%N)
  awk -v a="$t0" -v b="$t1" 'BEGIN{printf "boot->ssh %.1fs\n", b-a}'
}

stop() {
  gssh 'sync; reboot' 2>/dev/null || true
  for _ in $(seq 1 100); do pgrep -f "$FC --api-sock" >/dev/null || break; sleep 0.1; done
  pgrep -f "$FC --api-sock" >/dev/null && sudo pkill -f "$FC --api-sock" || true
  echo "stopped"
}

POLICY=/etc/jlp-fc-policy.nft
policy() {
  case "$1" in
  on|reload)
    sudo tee $POLICY >/dev/null <<'EOF'
table inet jlp_fc_policy
delete table inet jlp_fc_policy
table inet jlp_fc_policy {
  set deny4 { type ipv4_addr; flags interval; elements = {
    0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12,
    192.0.0.0/24, 192.0.2.0/24, 192.88.99.0/24, 192.168.0.0/16, 198.18.0.0/15,
    198.51.100.0/24, 203.0.113.0/24, 224.0.0.0/4, 240.0.0.0/4 } }
  chain forward_tap {
    type filter hook forward priority -10; policy accept;
    iifname "tap-fc" meta nfproto ipv6 reject with icmpx admin-prohibited
    iifname "tap-fc" ip daddr @deny4 meta l4proto tcp reject with tcp reset
    iifname "tap-fc" ip daddr @deny4 reject with icmpx admin-prohibited
    iifname "tap-fc" meta l4proto != { tcp, udp, icmp } reject with icmpx admin-prohibited
  }
  chain input_tap {
    type filter hook input priority -10; policy accept;
    iifname "tap-fc" ct state established,related accept
    iifname "tap-fc" ip saddr 172.16.0.2 tcp dport 2049 accept
    iifname "tap-fc" meta l4proto tcp reject with tcp reset
    iifname "tap-fc" reject with icmpx admin-prohibited
  }
}
EOF
    sudo nft -f $POLICY || { echo "policy load FAILED"; exit 1; }; sudo nft list table inet jlp_fc_policy >/dev/null && echo "policy loaded";;
  off) sudo nft delete table inet jlp_fc_policy 2>/dev/null || true; echo "policy off";;
  esac
}

cmd=$1; shift
case "$cmd" in
  prepare) prepare;;
  boot) boot;;
  stop) stop;;
  ssh) gssh "$@";;
  put) sudo scp -q "${SSHO[@]}" -r "$1" root@$GUEST:"$2";;
  get) sudo scp -q "${SSHO[@]}" -r root@$GUEST:"$1" "$2";;
  policy) policy "$1";;
  *) echo "unknown $cmd"; exit 2;;
esac
