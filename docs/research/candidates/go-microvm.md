# P1 spike: go-microvm

Spec: `vm-acceptance/1` ([vm-acceptance-spec.md](../vm-acceptance-spec.md)). Host: macOS 26.7, Apple M4 Pro, 24 GB RAM. Scripts and raw logs: `scripts/vm-spike/go-microvm/` (`01-install.sh`, `03-p1.sh`, `04-persist.sh`, `05-dind.sh`, `spike/main.go`, `spike-init.sh`, `logs/`, `results/`). `results-run1-partial/` and `02-boot.sh` are an earlier incomplete run (eth0 left down, Go sentinel) and are superseded.

## Summary

| Step | Result | Key number |
|---|---|---|
| 1. Install | PASS | `stacklok/go-microvm` at `7e148d8`, libkrun 1.19.7 + libkrunfw 5.6.2 (Homebrew `slp/krun`), runner codesigned with hypervisor entitlement |
| 2. Boot | PASS | `microvm.Run` returns in 5.6 s, guest init runs 1 s after launch |
| 3. Rootful dockerd, block-fs data-root | PASS with workarounds | Docker 29.9.2 needs `--firewall-backend=nftables` + `ip_forward=1`; data-root on loop-mounted ext4 image file |
| 4. Build jailoc image in guest | PASS | 41 s, 1.38 GB, no Dockerfile change |
| 5. Privileged Testcontainers + dind | PARTIAL | `TestRedisPing` PASS (5.4 s test, 35 s incl. pulls); stock `docker:dind` fails to start its daemon |
| 6. Persist / restart | PASS with workaround | needs `WithCleanDataDir`; launch → `docker info` 4–6 s; image and volume survive |
| 7. Probes (default networking) | FAIL (expected baseline) | PASS 574, FAIL 69, WARN 42, REVIEW 11, MISSING 0 |
| 8. Host processes / listeners | recorded | caller + one runner process per VM; VMM and network stack share the runner; TCP listener on 127.0.0.1 only |
| 9. P2 knobs | recorded | frame-level IPv4 firewall (CIDR/proto/port, default action) and hostname egress policy in the library |

The host-enforced filesystem boundary passes, including read-only remount and fresh mount. The network path fails on host addresses, LAN resolvers and rebinding, but not on the gateway. Two structural gaps: the root filesystem is itself a host virtio-fs share, and go-microvm exposes no block-device API.

## Environment

- Spike program `spike/main.go` calls `microvm.Run(ctx, "docker:29-dind", …)` with 4 vCPU, 4 GiB, `WithTmpSize(1024)`, `WithPorts({47823→4096})`, four `WithVirtioFS` mounts, `WithInitOverride("/bin/sh", "/spike-init.sh")`, a `RootFSHook` writing the init script, and `WithCleanDataDir`.
- Guest: Alpine-based `docker:29-dind` rootfs, kernel `6.12.109` (libkrunfw), cmdline `rootfstype=virtiofs … init=/init.krun`. NICs: `eth0` (virtio, `192.168.127.2/24` via DHCP, default via `192.168.127.1`) and a `dummy0` with no device. No global IPv6 (`-ipv6 disabled`). No `/proc/config.gz`.
- Shares (all virtio-fs): `/` (the rootfs clone), `/mnt/fx/rw` rw, `/mnt/fx/ro` `ReadOnly: true`, `/mnt/jlp` rw (probe binary, plan, build context, results), `/mnt/dstate` rw (Docker disk image). The last two are declared in the plan with `-export`.
- Disk: data dir `.spike-state/vm` 1.7 GB (image cache + rootfs clone), `docker.img` 5.3 GB used of a 30 GB sparse file.

## Steps

**2. Boot** (`logs/03.log`, `logs/03-vm-full.log`). With `WithInitOverride` the guest has `eth0` down, no address and no `/etc/resolv.conf`; go-microvm's own guest init (`guest/boot`) normally configures it. The spike runs `ip link set eth0 up; udhcpc -i eth0` and gets `192.168.127.2` from the in-process DHCP server. The image's packages are re-cloned every boot, so `apk add e2fsprogs nftables findmnt` runs each boot (1–4 s).

**3. dockerd** (`out-full/dockerd.log`). The libkrunfw kernel lacks the xtables extensions Docker's iptables backend needs (`addrtype`, `MASQUERADE`: "missing kernel module?"). `dockerd --firewall-backend=nftables` starts once `nft` is installed and `net.ipv4.ip_forward=1` is set. `docker info`: `driver=overlayfs version=29.9.2 root=/var/lib/docker`. go-microvm has no option to attach a block device (the `krun` bindings have `AddDisk`, the runner does not use it), so `/var/lib/docker` is an ext4 filesystem in a sparse file on the `dstate` share, loop-mounted in the guest (`/dev/loop0 ext4`).

**4–5. Workload** (`out-full/build.log`, `out-full/tc.log`). `docker build` of `internal/embed/assets` succeeds in 41 s (1.38 GB). `docker run --privileged -v /var/run/docker.sock … golang:1.26 go test` → `--- PASS: TestRedisPing (5.38s)`. Nested `docker run --privileged docker:dind` fails: its inner dockerd hits the same missing xtables extensions, and with `--firewall-backend=nftables` it lacks the `nft` binary (`logs/05.log`, `out-dind/marks.txt`). A jailoc DinD image would need `nftables` installed and the nftables backend selected.

**6. Persistence** (`logs/04.log`). Without `WithCleanDataDir`, a second `microvm.Run` on the same data dir fails with `clone rootfs: symlink … rootfs-work/bin/arch: file exists` because `Stop` leaves `rootfs-work/` behind. With it, two stop/start cycles each print `image-survived e71c90f0b175` and the volume marker `persisted-1793014530`. Launch → dockerd ready: 4 s and 6 s; stop ≤ 1 s. Anything written to the rootfs outside the loop disk is discarded every boot.

## Probe baseline

Sentinel: `sentinel.py` under `/usr/bin/python3` (Go sentinel is dropped by the macOS application firewall). Self-test reaches every host address except the tunnel IPv6 address.

Verdict totals: PASS 574, FAIL 69, WARN 42 (link-local self-test), REVIEW 11, MISSING 0.

| Check | FAILs | Cause |
|---|---|---|
| `N-HOST-TCP`, `N-HOST-UDP` | 24, 12 | every context reaches the Mac on `10.0.0.73`, `10.8.0.160`, `192.168.100.3`; the runner opens host UDP sockets to these addresses (`lsof`) |
| `D-PRIV-RESOLVER-*` | 24 | private LAN resolvers `10.0.53.124/125`, `10.0.54.181` answer |
| `D-REBIND-TCP` | 4 | `10-4-68-73.sslip.io` connects to the host |
| `F-MNT-INV` | 1 | `/` is an undeclared virtio-fs share (`/dev/root`, the host directory `.spike-state/vm/rootfs-work`) |
| `F-HOST-OUTSIDE-LISTING` | 1 | spike artifact: the fixture directory held canaries from three `plan` runs; not a guest write |
| `S-SELFTEST-HOST-*` | 3 | tunnel IPv6 address unreachable from the host itself |

Passing items worth noting: `N-GW-*` (the gateway `192.168.127.1` does not forward to host loopback), no runtime host alias resolves (`N-ALIAS-*` not generated), `N-META-TCP`, `N-V6-DISABLED-*`, `N-PUB-QUIC`. All other `F-` checks: `ReadOnly` virtio-fs is enforced by libkrun's host-side virtio-fs server, including `F-RO-REMOUNT` and `F-RO-FRESHMOUNT`; `F-SHARE-SCOPE` passes on both fixtures, so a share does not expose its parent directory; `F-HOST-SUID` passes. `I-NIC`, `I-V6`, `I-VSOCK` (no host vsock listener reachable), `L-INGRESS-*` (forward on `127.0.0.1:47823` only), `L-FAILCLOSED-DENY` (30 s, no broker kill).

REVIEW: `D-RESOLVER-CLASS` (`192.168.127.1`, the gvisor-tap-vsock DNS in the runner), `D-REBIND-RESOLVE`, `F-RW-SUID`, `F-RW-MKNOD`, `L-FAILCLOSED-PUBLIC`.

## Host processes and listeners

- Two processes per VM, both user `<host-user>`: the caller (`.spike-state/spike`, links go-microvm) and `go-microvm-runner`, which runs libkrun (VMM, virtio-fs server) **and** the gvisor-tap-vsock network stack in one process. The VMM and the network broker therefore share one host authority.
- Runner entitlements: `com.apple.security.hypervisor`, `cs.disable-library-validation`, `cs.allow-dyld-environment-variables`. No App Sandbox.
- TCP listener: `127.0.0.1:47823` (port forward) only. UDP: one host socket per guest flow (NAT), bound to the egress interface.
- State: `go-microvm-state.json` (pid, image, resources), `go-microvm-state.lock`, `console.log`, `vm.log` in the data dir; no control socket.

## P2 network-policy knobs

From `docs/NETWORKING.md` in the library:

- `WithFirewallRules(...)` + `WithFirewallDefaultAction(...)`: frame-level filter in the relay between the guest NIC and the virtual network, ordered first-match rules on direction, protocol, source/destination CIDR and single port, with stateful connection tracking (TCP 5 min, UDP 30 s). Non-IPv4 frames (ARP, **IPv6**) bypass the filter, so IPv6 must stay absent from the virtual network.
- `WithEgressPolicy(...)`: hostname allowlist that answers NXDOMAIN for other names and turns snooped answers into temporary rules — this is DNS-as-authorization, which the spec does not accept as the policy, but the deny path could sit on top of CIDR rules.
- `WithNetProvider(...)` / `net/hosted`: moves the network stack into the caller process, which would separate the broker from the VMM process.

Conclusion for P2: go-microvm has a usable in-library policy hook (CIDR deny list + allow-all default with the spec's hard-deny ranges) and a way to split the broker from the VMM. P2 must test the firewall against `N-HOST-*`, `D-PRIV-RESOLVER-*` and `D-REBIND-TCP`.

## Blockers and open questions

- Root filesystem is a host virtio-fs share of `rootfs-work/` (`F-MNT-INV`). It is VM-private, but guest root writes host files through it, and the spec requires Docker state on a block filesystem; the loop-on-virtio-fs workaround meets "block filesystem" only inside the guest.
- No block-device option in `microvm.Run`; a real data disk needs an upstream change or a fork of the runner.
- libkrunfw kernel: no xtables NAT/addrtype; Docker and DinD must use the nftables backend, and the jailoc DinD image must ship `nft`.
- `WithInitOverride` skips network setup; jailoc would either use go-microvm's `guest/boot` init or replicate DHCP.
- Linux/KVM not tested (macOS only).
- Peer workspace and LAN sentinel not configured.
