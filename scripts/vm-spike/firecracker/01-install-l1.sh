#!/bin/bash
set -euxo pipefail
base=/path/to/jailoc-firecracker/scripts/vm-spike/firecracker
mkdir -p "$base/artifacts"
cd "$base/artifacts"
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y curl jq squashfs-tools e2fsprogs nftables nfs-kernel-server sshpass socat
curl -fsSL https://api.github.com/repos/firecracker-microvm/firecracker/releases/latest -o release.json
tag=$(jq -r .tag_name release.json)
curl -fL --retry 2 "https://github.com/firecracker-microvm/firecracker/releases/download/$tag/firecracker-$tag-aarch64.tgz" -o firecracker.tgz
curl -fL --retry 2 "https://github.com/firecracker-microvm/firecracker/releases/download/$tag/firecracker-$tag-aarch64.tgz.sha256.txt" -o firecracker.sha256
sha256sum firecracker.tgz
cat firecracker.sha256
expected=$(grep -oE '[a-fA-F0-9]{64}' firecracker.sha256 | head -1)
printf '%s  %s\n' "$expected" firecracker.tgz | sha256sum -c -
tar -xzf firecracker.tgz
find "release-$tag-aarch64" -maxdepth 1 -type f -print
"$PWD/release-$tag-aarch64/firecracker-$tag-aarch64" --version
S3=https://s3.amazonaws.com/spec.ccfc.min
curl -fsSL "$S3?list-type=2&prefix=firecracker-ci/&delimiter=/" -o ci-prefix.xml
prefix=$(grep -oP '(?<=<Prefix>)firecracker-ci/[0-9]{8}-[^/]+/(?=</Prefix>)' ci-prefix.xml | sort | tail -1)
test -n "$prefix"
curl -fsSL "$S3?list-type=2&prefix=${prefix}aarch64/vmlinux-" -o ci-kernels.xml
kernel=$(grep -oP '(?<=<Key>)[^<]*vmlinux-[0-9]+\.[0-9]+\.[0-9]{1,3}(?=</Key>)' ci-kernels.xml | sort -V | tail -1)
curl -fsSL "$S3?list-type=2&prefix=${prefix}aarch64/ubuntu-" -o ci-ubuntu.xml
rootfs=$(grep -oP '(?<=<Key>)[^<]*ubuntu-[0-9]+\.[0-9]+\.squashfs(?=</Key>)' ci-ubuntu.xml | sort -V | tail -1)
test -n "$kernel" && test -n "$rootfs"
printf 'kernel=%s rootfs=%s\n' "$kernel" "$rootfs" | tee ci-selected.txt
curl -fL --retry 2 "$S3/$kernel" -o vmlinux
curl -fL --retry 2 "$S3/$rootfs" -o ubuntu.squashfs
sha256sum vmlinux ubuntu.squashfs | tee ci-sha256.txt
unsquashfs -d squashfs-root ubuntu.squashfs
grep -E 'CONFIG_(OVERLAY_FS|NF_TABLES|BRIDGE|VETH|CGROUPS|CGROUP2_FS)=' "$(find squashfs-root/boot -type f -name 'config-*' | head -1)" || true
ls -lh vmlinux ubuntu.squashfs
printf 'Summary: release=%s CI=%s kernel=%s rootfs=%s\n' "$tag" "$prefix" "$kernel" "$rootfs"
