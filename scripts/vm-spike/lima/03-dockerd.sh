#!/usr/bin/env bash
# Step 3: rootful Docker Engine in the guest, data-root on the ext4 root disk.
set -euo pipefail
cd "$(dirname "$0")/../../.."
D=scripts/vm-spike/lima
exec > >(tee "$D/logs/03.log") 2>&1
set -x
t0=$(date +%s)
limactl shell jlp-lima -- sudo bash -euxc '
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -q
  apt-get install -y -q ca-certificates curl
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  echo "deb [arch=arm64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable" > /etc/apt/sources.list.d/docker.list
  apt-get update -q
  apt-get install -y -q docker-ce docker-ce-cli containerd.io docker-buildx-plugin
  systemctl enable --now docker
  usermod -aG docker lima || true
'
t1=$(date +%s)
limactl shell jlp-lima -- sudo bash -c 'docker version --format "{{.Server.Version}}"; docker info --format "driver={{.Driver}} root={{.DockerRootDir}} cgroup={{.CgroupVersion}} rootless={{range .SecurityOptions}}{{.}} {{end}}"; findmnt -T /var/lib/docker; ps -o user,cmd -C dockerd'
set +x
echo "== SUMMARY step3: docker install $((t1-t0))s"
