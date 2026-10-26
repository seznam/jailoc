#!/bin/bash
set -euxo pipefail
clone=/path/to/jailoc-firecracker
base=$clone/scripts/vm-spike/firecracker
art=$base/artifacts
results=$base/results
fix=$clone/.spike-state/fx
mkdir -p "$results"
host_ip=$(getent ahostsv4 host.lima.internal | awk 'NR==1 {print $1}')
test -n "$host_ip"
printf 'macOS L1-visible=%s\n' "$host_ip"
"$clone/bin/vmprobe-linux" plan -candidate firecracker -dir "$fix" -guest-prefix /mnt/fx -port 47841 -alt-port 47842 -lan-sentinel "$host_ip" -ipv6 disabled -out "$results/plan.json"
"$clone/bin/vmprobe-linux" sentinel -plan "$results/plan.json" -name host -log "$results/hits.jsonl" > "$base/logs/l1-sentinel.log" 2>&1 &
printf '%s\n' "$!" > "$results/l1-sentinel.pid"
sleep 1
"$clone/bin/vmprobe-linux" selftest -plan "$results/plan.json" -out "$results/r-selftest.json"
sudo mkdir -p /etc/exports.d
printf '%s 172.16.0.2/32(rw,sync,no_subtree_check,fsid=0,no_root_squash)\n' "$fix/rw" | sudo tee /etc/exports.d/jlp-fc.exports
sudo exportfs -rav
sudo exportfs -v
sudo systemctl restart nfs-server
if [ ! -f "$art/root.ext4" ]; then
  sudo mkdir -p "$art/squashfs-root/etc/systemd/network" "$art/squashfs-root/mnt/fx/ro" "$art/squashfs-root/mnt/fx/rw" "$art/squashfs-root/mnt/docker"
  printf '[Match]\nName=eth0\n[Network]\nAddress=172.16.0.2/30\nGateway=172.16.0.1\nDNS=1.1.1.1\n' | sudo tee "$art/squashfs-root/etc/systemd/network/10-eth0.network"
  sudo sed -i '/^root:/s|^root:[^:]*:|root:'"$(openssl passwd -6 root)"':|' "$art/squashfs-root/etc/shadow"
  sudo mkdir -p "$art/squashfs-root/etc/ssh/sshd_config.d"
  printf 'PermitRootLogin yes\nPasswordAuthentication yes\n' | sudo tee "$art/squashfs-root/etc/ssh/sshd_config.d/99-spike.conf"
  printf '/dev/vdb /mnt/docker ext4 defaults 0 0\n/dev/vdc /mnt/fx/ro ext4 ro,noload 0 0\n172.16.0.1:/ /mnt/fx/rw nfs4 rw,nosuid,nodev 0 0\n' | sudo tee -a "$art/squashfs-root/etc/fstab"
  sudo truncate -s 2G "$art/root.ext4"
  sudo mkfs.ext4 -F -d "$art/squashfs-root" "$art/root.ext4"
fi
if [ ! -f "$art/data.ext4" ]; then truncate -s 6G "$art/data.ext4"; mkfs.ext4 -F "$art/data.ext4"; fi
if [ ! -f "$art/ro.ext4" ]; then sudo truncate -s 32M "$art/ro.ext4"; sudo mkfs.ext4 -F -d "$fix/ro" "$art/ro.ext4"; fi
sudo ip tuntap add dev tap-fc mode tap
sudo ip addr add 172.16.0.1/30 dev tap-fc
sudo ip link set dev tap-fc up
sudo sysctl -w net.ipv4.ip_forward=1
sudo nft add table ip jlp_fc_nat
sudo nft 'add chain ip jlp_fc_nat postrouting { type nat hook postrouting priority srcnat; policy accept; }'
sudo nft add rule ip jlp_fc_nat postrouting ip saddr 172.16.0.2 masquerade
tag=$(jq -r .tag_name "$art/release.json")
firecracker="$art/release-$tag-aarch64/firecracker-$tag-aarch64"
cat > "$art/vm.json" <<EOF
{"boot-source":{"kernel_image_path":"$art/vmlinux","boot_args":"console=ttyS0 reboot=k panic=1 keep_bootcon root=/dev/vda rw"},"drives":[{"drive_id":"rootfs","path_on_host":"$art/root.ext4","is_root_device":true,"is_read_only":false},{"drive_id":"dockerdata","path_on_host":"$art/data.ext4","is_root_device":false,"is_read_only":false},{"drive_id":"fixturesro","path_on_host":"$art/ro.ext4","is_root_device":false,"is_read_only":true}],"network-interfaces":[{"iface_id":"net1","guest_mac":"06:00:AC:10:00:02","host_dev_name":"tap-fc"}],"machine-config":{"vcpu_count":2,"mem_size_mib":2048}}
EOF
"$firecracker" --api-sock "$art/firecracker.sock" --config-file "$art/vm.json" > "$base/logs/l2-console.log" 2>&1 &
printf '%s\n' "$!" > "$results/firecracker.pid"
for i in $(seq 1 60); do if sshpass -p root ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=2 root@172.16.0.2 true; then break; fi; sleep 2; done
sshpass -p root ssh -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@172.16.0.2 'uname -a; ip -br addr; findmnt /mnt/docker /mnt/fx/ro /mnt/fx/rw; zcat /proc/config.gz 2>/dev/null | grep -E "CONFIG_(OVERLAY_FS|NF_TABLES|BRIDGE|VETH|CGROUPS|CGROUP2_FS)=" || true'
printf 'Summary: L2 booted via %s; TAP tap-fc; NFS export=%s/rw; host=%s\n' "$tag" "$fix" "$host_ip"
