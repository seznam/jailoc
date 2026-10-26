# P1 spike: Firecracker

Spec: `vm-acceptance/1` ([vm-acceptance-spec.md](../vm-acceptance-spec.md)). Host: macOS 26.7, Apple M4 Pro, 24 GB RAM. Firecracker runs only on Linux/KVM, so the spike uses a nested topology: **L1** = Lima VM `jlp-fc-host` (`vz`, `nestedVirtualization: true`, Ubuntu 24.04, 6 vCPU / 8 GiB) acts as the Linux host; **L2** = the Firecracker microVM (the workspace). Scripts and raw logs: `scripts/vm-spike/firecracker/` (`fc.sh` host-side helper run in L1, `03-06.sh`, `04-build-retry.sh`, `07-08.sh`, `logs/`, `results-*/`).

Timing numbers are **not representative**: nested virtualization on Apple Silicon plus host memory pressure (macOS swap 9 GB of 10 GB used during the runs) made the guest 10–20× slower than L1. P4 measurements need bare-metal Linux KVM.

## Summary

| Step | Result | Key number |
|---|---|---|
| 0. KVM | PASS | `/dev/kvm` usable in L1 |
| 1. Install | PASS | Firecracker v1.17.0 aarch64 release (sha256 verified), CI kernel 6.18.54 + Ubuntu 24.04 squashfs, 79 s |
| 2. Boot | PASS | boot → SSH 32–41 s uncontended (kernel ≈ 8 s), 80 s+ under host swap |
| 3. Rootful dockerd, block-fs data-root | PASS (after layout fix) | Docker 29.9.2, overlayfs, `/var/lib/docker` and `/var/lib/containerd` on `/dev/vdb` ext4 |
| 4. Build jailoc image in guest | BLOCKED (environment) | BuildKit session health check times out in the starved guest; full image not built |
| 5. Privileged Testcontainers + dind | PARTIAL | `docker:dind` privileged → `dind-ok 29.9.2 overlayfs`; Testcontainers run not completed (guest starved) |
| 6. Persist / restart | PASS | image store on `/dev/vdb` survived a hard VMM kill and reboot (`docker:dind` pulled before the reboot still present) |
| 7. Probes, no policy | FAIL (expected baseline) | PASS 218, FAIL 117, WARN 1, REVIEW 21, MISSING 0 |
| 7. Probes, nftables policy on the TAP | PASS except 2 probe artifacts | PASS 306, FAIL 5, WARN 1, REVIEW 21, MISSING 0 |
| 8. Host processes / listeners | recorded | one root `firecracker` process; NFS server listens on all L1 interfaces |
| 9. P2 knobs | recorded | none in Firecracker; host nftables on the TAP is the whole policy |

Firecracker itself provides no network policy and no file sharing. With a host nftables policy on the TAP, every network deny check passes in all four contexts, including during ten atomic ruleset reloads. The filesystem boundary passes with NFSv4 (`all_squash`) for the read-write share and a read-only virtio-blk image for the read-only share.

## Environment

- L1: Lima 2.2.1, `vmType: vz`, `nestedVirtualization: true`, Ubuntu 24.04 kernel `6.8.0-147-generic` aarch64. The clone is mounted into L1 at the same path. L1's Docker is stopped and its `ip filter`/`ip6 filter` tables deleted so they do not interfere with forwarding.
- Firecracker v1.17.0 aarch64 from the GitHub release; guest kernel `vmlinux-6.18.54` and `ubuntu-24.04.squashfs` from the Firecracker CI artifacts bucket (`firecracker-ci/20261023-dd7c24b0d165-0`). The kernel config has overlayfs, nf_tables, iptables NAT/addrtype/MASQUERADE, bridge netfilter, veth, cgroups, user namespaces, NFSv4, vsock, FUSE and TUN — Docker-capable without rebuilds.
- `vm.json`: 4 vCPU, 4096 MiB, drives `root.ext4` (4 GB rw), `docker.ext4` (16 GB rw), `ro.ext4` (64 MB, `is_read_only: true`), one virtio-net on `tap-fc` (`172.16.0.1/30` host side, guest `172.16.0.2`), no vsock, no MMDS. Started with `firecracker --api-sock … --config-file vm.json` as root in L1 (no jailer).
- Guest networking: L1 masquerades `172.16.0.2` out of `eth0` (Lima usernet). The CI rootfs's `fcnet` only assigns the address, so a unit adds the default route. Guest DNS: `systemd-resolved` stub forwarding to `1.1.1.1`.
- Shares: `/mnt/fx/rw` = NFSv4.2 from L1 (`rw,all_squash,anonuid=502,anongid=1000`, export limited to `172.16.0.2`); `/mnt/fx/ro` = read-only virtio-blk ext4 image built from the fixture at prepare time (`ro,noload`). No other shared filesystem.

## Steps

**2. Boot** (`logs/` of the prepare/boot runs in `PROGRESS.md`): `fc.sh boot` → SSH reachable after 32–41 s in early runs (`systemd-analyze`: kernel ≈ 7.8 s, userspace 18–25 s). Later boots under host swap took 80 s to reach early systemd units and several minutes for `containerd`. Rootfs fixes needed on the CI image: default route, `mount.nfs` (copied from L1), `gpgv` (missing; first `apt` run bootstrapped with `AllowInsecureRepositories` for the Ubuntu archive only, Docker packages from the signed Docker repo).

**3. dockerd** (`logs/03-06-run*.log`): Docker CE 29.9.2 from `download.docker.com`, 483 s for the first `apt` run in the nested guest. Two install failures: a `procps` conffile prompt (fixed with `--force-confold`) and `systemd-resolved` pulled in by dependencies with no uplink (fixed with a `DNS=1.1.1.1` drop-in). `dockerd` runs as root, overlayfs, cgroup v2, iptables backend works in the guest.

Docker 29 uses the containerd image store by default, so image content lands in `/var/lib/containerd` on the root disk, not on the `data-root` block disk. The 4 GB root disk was 66 % full after pulling `docker:dind`, `alpine` and the cached build layers. The fix binds `/var/lib/docker/containerd` onto `/var/lib/containerd` (both on `/dev/vdb`). A jailoc VM backend has to put **both** paths on the persistent data disk.

**4. Build** (`logs/03-06-run3-npm-fail.log`, `logs/04-build-retry.log`): not completed. Run 3 failed after ~25 min at the Dockerfile's `npm install -g` language-server layer (`-q` output; most likely the full 4 GB root disk described above). After the disk-layout fix, two detached retries failed within 96 s with BuildKit errors (`DeadlineExceeded: context deadline exceeded`, `NotFound: forwarding Ping: no such job`). A two-line `FROM alpine` / `RUN echo hi` Dockerfile reproduced it: the `RUN` step took 48 s and the build was cancelled when `dockerd` logged `session healthcheck failed fatally`. The legacy builder (`DOCKER_BUILDKIT=0`) built the same Dockerfile in 63 s. The failure is the starved nested environment, not Firecracker: macOS swap reached 11.9 of 12 GB during the retry, and the spike stopped here to protect the host.

**5. Testcontainers / dind** (`logs/03-06-run3-npm-fail.log`): `docker run -d --privileged docker:dind` → `dind-ok 29.9.2 overlayfs`, nested `alpine` pulled. The `golang:1.26` + `TestRedisPing` run did not complete: the guest stopped answering SSH (banner timeout) while the build and dind ran concurrently, with `firecracker` at ~380 % CPU in L1.

**6. Persistence**: the VMM was killed (`pkill`) and the VM rebooted twice with `/dev/vdb` holding both `/var/lib/docker` and `/var/lib/containerd`. After the last reboot `docker image ls` still listed `docker:dind` (pulled before the reboot), `dockerd` restored its state (the stale `docker:dind` container record from the killed run was removed offline before booting). Cached-start timing is not meaningful here (minutes under swap).

## Probe results

L1 is the host for the plan (`-lan-sentinel 10.0.0.73 -lan-sentinel 192.168.5.2`, IPv6 `disabled`). Sentinels: `vmprobe-linux sentinel` in L1, `sentinel.py` on the Mac. Guest probes ran with `-timeout 10s` (watch `-timeout 5s`) instead of the default 3 s: with 3 s, every public positive control timed out in the starved guest (`results-baseline-run1/`), while `curl https://1.1.1.1` from the guest took 6.4 s for the first request and 0.1 s afterwards. The unprivileged context uses the rootfs's existing `ubuntu` user (uid 1000).

### No policy (`results-baseline/`)

PASS 218, FAIL 117, WARN 1, REVIEW 21, MISSING 0. A bare TAP plus masquerade is the rejected "unfiltered TAP": every private destination is reachable from every context.

- `N-HOST-*`, `N-GW-*`: L1's own addresses including `172.16.0.1` and its IPv6 link-local address on `tap-fc` (reached over `fe80::…%eth0` from the guest).
- `N-LAN-*`, `N-LANGW-TCP`: the Mac (`10.0.0.73`, `192.168.5.2`) and L1's sshd.
- `D-REBIND-TCP`: `192-168-5-2.sslip.io` resolves and connects.
- `L-FAILCLOSED-*`: every watch tick leaked.

### nftables policy on the TAP (`results-policy/`)

`fc.sh policy on` loads one atomic `table inet jlp_fc_policy`:

- `forward` hook, priority −10, `iifname tap-fc`: reject all IPv6; reject destinations in the spec's IPv4 deny set (TCP reset, ICMP admin-prohibited otherwise); reject non-TCP/UDP/ICMP.
- `input` hook, priority −10, `iifname tap-fc`: accept established/related (host-initiated SSH control), accept TCP 2049 from `172.16.0.2` (NFS share), reject everything else.

PASS 306, FAIL 5, WARN 1, REVIEW 21, MISSING 0. `N-HOST`, `N-GW`, `N-LAN`, `N-LANGW-DNS`, `N-META`, `N-V6-DISABLED`, `D-HOST-DNS`, `D-PRIV-RESOLVER`, `D-REBIND-TCP` and the public positive controls (TLS, DNS UDP/TCP, QUIC) pass in every context where they ran (the plan has no peer workspace, so `N-PEER` did not run). During the 60 s watch the ruleset was reloaded ten times (`nft -f` with delete+create in one transaction): `L-FAILCLOSED-DENY` and `L-FAILCLOSED-SENTINEL` PASS, public reachable in 60/60 ticks.

The five FAILs are probe artifacts, not leaks:

- `N-LANGW-TCP 172.17.0.1:22` (4 contexts): `172.17.0.1` is L1's (down) `docker0` address, so the plan treats it as a host prefix gateway, but inside the guest it is the guest's own `docker0` address; the banner is the guest's sshd.
- `S-SELFTEST-HOST-UDP 172.17.0.1`: same address, interface down in L1.

A first policy run (`results-policy-run1-invalid/`) used chain names `fwd`/`inp`; `fwd` is an nftables keyword, `nft -f` rejected the file, and the driver continued with no policy (results equal to baseline). The driver now aborts when the policy fails to load. A real backend has to make "policy failed to load" mean "no forwarding", e.g. by enabling the masquerade/forwarding only inside the same table as the deny rules.

### Filesystem and interfaces

Identical in both runs. All `F-RW-*`, `F-RO-*` (create, write, truncate, chmod, chown, utimes, xattr, mkdir, symlink, rename, unlink, remount), `F-MNT-INV`, `F-OUTSIDE-*`, `F-SOCK`, `F-HOST-*` PASS.

- Read-write NFS: guest root's `chown 0:0` fails (`all_squash`), files land as uid 502 (the L1 user), no device node (`mknod` EPERM), symlinks and `..` stay inside the export.
- Read-only image: `mount -o remount,rw` fails because the virtio-blk device is read-only at the VMM. `F-RO-FRESHMOUNT` and `F-SHARE-SCOPE` are `REVIEW` and `F-MNT-DECLARED` `WARN` because the export is a block image, not a shared filesystem; the image is a snapshot of the fixture, not a live view of host files.
- `I-NIC` (one virtio NIC), `I-ROUTE-V4`, `I-V6` (no global IPv6, no IPv6 default route), `I-VSOCK` (no vsock device) PASS. `L-INGRESS-LOOPBACK`/`-EXTERNAL` PASS with an L1 `socat` on `127.0.0.1:47843 → 172.16.0.2:4096` standing in for the jailoc port forwarder.

## Host processes and listeners

Inventory from L1 (`logs/07-08-*.log`, step 8):

- One `firecracker` process per VM, running as root (no jailer in the spike), API socket `/var/lib/jlp-fc/api.sock`. Production would run it under the jailer (chroot, namespaces, seccomp, dropped uid) with the TAP pre-created by a privileged helper.
- Kernel NFS server for the read-write share: `nfsd` on `0.0.0.0:2049`/`[::]:2049`, `rpcbind` `:111`, `rpc.mountd`/`rpc.statd` on random ports, all interfaces. Reachability from the guest is limited only by the nftables `input` rule. A production share would bind to the TAP address only, or use a userspace server per workspace.
- No other listener is created by the candidate.

## P2 network-policy knobs

Firecracker has no egress policy, no DNS control and no userspace network stack: the guest NIC is a host TAP, and policy is whatever the host does with packets from that TAP (nftables here, or a userspace stack such as passt/gvisor-tap-vsock attached to the TAP). Rate limiters exist per device but are not policy. MMDS (`169.254.169.254`) must stay unconfigured. Creating TAP devices and loading nftables rules needs `CAP_NET_ADMIN` on the host, so a jailoc backend needs a privileged helper even though the VMM itself can run unprivileged.

## Blockers and open questions

- **macOS**: not supported. Firecracker can only be the Linux half of a backend, paired with a different macOS backend that passes the spec on its own.
- **File sharing**: no virtio-fs or 9p. The read-write share needs a host file server (kernel NFS here, which brings host listeners and `all_squash` uid mapping; alternatives are a userspace NFS/9p server over vsock). Read-only shares as block images are snapshots and need a rebuild when host files change.
- **Root disk layout**: both `/var/lib/docker` and `/var/lib/containerd` must live on the persistent data disk with Docker 29.
- **CI artifacts are not a product**: the kernel and rootfs come from Firecracker's CI bucket. jailoc would need to build and pin its own kernel and rootfs (with `mount.nfs`, `gpgv`, a uid-1000 `agent` user).
- **Fail-closed design**: nftables rules are kernel state, there is no broker process to crash, but removing or failing to load the table opens the TAP. Tie forwarding/NAT to the policy table.
- **Performance**: not measurable on this host. Re-run steps 2–6 and P4 on bare-metal Linux KVM.
