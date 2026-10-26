#!/usr/bin/env bash
# Step 2: create and boot the Lima workspace VM (vz, one NIC, two fixture shares, no home mount).
set -euo pipefail
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/lima
FX=$PWD/.spike-state/fx
exec > >(tee "$D/logs/02.log") 2>&1
set -x
limactl --version
df -h / | tail -1
t0=$(date +%s)
limactl create --tty=false --name=jlp-lima --vm-type=vz --cpus=4 --memory=4 --disk=30 \
  --containerd=none --mount-type=virtiofs \
  --set ".mounts = [{\"location\": \"$FX/rw\", \"mountPoint\": \"/mnt/fx/rw\", \"writable\": true}, {\"location\": \"$FX/ro\", \"mountPoint\": \"/mnt/fx/ro\", \"writable\": false}]" \
  --set '.portForwards = [{"guestPort": 4096, "hostIP": "127.0.0.1", "hostPort": 47813}, {"guestPortRange": [1, 65535], "ignore": true}]' \
  template:ubuntu-24.04
t1=$(date +%s)
limactl start --tty=false jlp-lima
t2=$(date +%s)
limactl stop jlp-lima
t3=$(date +%s)
limactl start --tty=false jlp-lima
t4=$(date +%s)
cp ~/.lima/jlp-lima/lima.yaml "$D/lima.yaml"
limactl shell jlp-lima -- bash -c 'uname -a; cat /etc/os-release | head -2; ip -br addr; ip route; ip -6 route; cat /etc/resolv.conf | grep -v "^#"; ls /sys/class/net; for n in /sys/class/net/*; do echo "$n -> $(readlink -f $n/device 2>/dev/null)"; done; findmnt -t virtiofs,9p,fuse.sshfs; lsblk -f; id'
set +x
echo "== SUMMARY step2"
echo "create(incl image download)=$((t1-t0))s first-start=$((t2-t1))s stop=$((t3-t2))s warm-start=$((t4-t3))s"
du -sh ~/.lima/jlp-lima ~/Library/Caches/lima 2>/dev/null || true
