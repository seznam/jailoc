# Debian guest with systemd

Spec: `vm-acceptance/2` ([vm-acceptance-spec.md](vm-acceptance-spec.md)). Question: can a workspace VM run a full distribution with systemd as PID 1, behind `jailoc-netbroker`, with Docker on the virtio-blk data disk? Harness: `scripts/vm-spike/debian/` (`debian.sh` host driver, `stage1.sh` image preparation, `verify.sh` boot-time checks) with the multi-workspace launcher from `scripts/vm-spike/multi/launcher`.

## Result

Yes. Two consecutive boots of a stock `debian:trixie` image, prepared once, reached a running systemd with no failed units:

| Check | Result |
|---|---|
| PID 1 | `/usr/lib/systemd/systemd` |
| OS, kernel | Debian 13.7, libkrun kernel 6.12.109 |
| Privileges | uid 0, `CapEff 000001ffffffffff` |
| Network | `systemd-networkd` DHCP lease from the broker, `systemd-resolved` via the broker gateway |
| Packages | `apt-get install jq` from `deb.debian.org` |
| Data disk | `/var/lib/docker` = `/dev/vda` ext4 (virtio-blk, 10 GiB), mounted from `fstab` |
| Docker | Docker CE 29.9.2, overlayfs, cgroup v2 with systemd driver, privileged container can create links, container egress to `https://example.com` |
| Persistence | image pulled in boot 1 present in boot 2 |
| Probe (`guest-root`, net/dns/iface) | PASS 172, REVIEW 2 on each boot |
| Supervision | guest `poweroff` → `supervise-nic-disconnected`, broker stopped, launcher exit 0 |
| Timing (macOS) | broker ready 134 ms, VM started 139 ms, NIC connected 510 ms; whole boot including `apt`, image pull and probe 37–42 s |

The only journal error is `Failed to find module 'autofs4'`: the libkrun kernel has no loadable modules.

## Preparation

`stage1.sh` runs once as PID 1 of the stock image (libkrun init, static network) and installs, with `--no-install-recommends`: `systemd systemd-sysv systemd-resolved udev dbus iproute2 ca-certificates curl nftables e2fsprogs procps util-linux`, then Docker CE (`docker-ce docker-ce-cli containerd.io`) from `download.docker.com`. It configures:

- `systemd-networkd` with DHCPv4 on `eth0`, IPv6 router advertisements and link-local addressing off;
- `systemd-resolved` and the stub `resolv.conf`;
- `systemd-modules-load` masked;
- `/etc/docker/daemon.json` `{"firewall-backend":"nftables"}` and `net.ipv4.ip_forward=1`;
- `fstab`: the virtio-fs share and `/dev/vda` on `/var/lib/docker`, both `nofail`, fsck pass `0`.

Package installation takes about 22 s.

## Requirements found

- **systemd as PID 1:** libkrun's init runs the workload as its child unless `KRUN_INIT_PID1=1` is in the init environment; then it `exec`s the workload as PID 1. The environment only reaches init through `krun_set_env`, which go-microvm's runner does not call. The spike used a local runner patch that forwards a variable list; upstream needs an option for it.
- **udev:** without it, device units (`dev-vda.device`, `dev-hvc0.device`) time out and the boot drops to emergency mode.
- **fsck:** `systemd-fsck` fails on `/dev/vda`; the data disk uses fsck pass `0`.
- **Docker firewall backend:** Debian's `docker.io` 26 uses iptables and fails to insert NAT rules because the libkrun kernel lacks the xtables NAT targets. Docker CE with `firewall-backend: nftables` works.
- **Data disk size:** libkrun's block layer (the `imago` crate before 0.2.5) handles a discard or write-zeroes request that reaches the end of the image by truncating the file at the request offset. `mkfs.ext4` zeroes the device tail even with `-E nodiscard`, so a fresh 10 GiB image shrinks by 64 KiB and the next mount fails with `bad geometry: block count exceeds size of device`. `imago` 0.2.5 keeps the image size; until libkrun ships it, the launcher grows the image back to its configured size before every boot.

## Gaps

- `systemctl is-system-running` reports `starting` while the verification unit runs, because that unit is part of the boot transaction; boot completion was confirmed from the journal, not from `multi-user.target` timing.
- Boot times are macOS numbers with package installation and network-dependent steps included; a bare boot was not measured separately.
- Only `guest-root` network checks ran; the four-context run is in [p2-broker.md](p2-broker.md).
