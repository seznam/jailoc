# P1 spike: Lima

Spec: `vm-acceptance/1` ([vm-acceptance-spec.md](../vm-acceptance-spec.md)). Host: macOS 26.7, Apple M4 Pro, 24 GB RAM. Scripts and raw logs: `scripts/vm-spike/lima/` (`02-boot.sh`, `03-dockerd.sh`, `04-06-workload.sh`, `07-08-probes.sh`, `logs/`, `results/`).

## Summary

| Step | Result | Key number |
|---|---|---|
| 1. Install | PASS | Lima 2.2.1 (Homebrew), vmType `vz` |
| 2. Boot | PASS | create 23 s (image cached), first start 9 s, warm start 11 s |
| 3. Rootful dockerd, block-fs data-root | PASS | Docker 29.9.2, overlayfs on `/dev/vda1` ext4 |
| 4. Build jailoc image in guest | PASS | 41 s, 1.38 GB, no Dockerfile change |
| 5. Privileged Testcontainers + dind | PASS | `TestRedisPing` PASS (4.9 s test, 35 s incl. pulls); dind + nested run OK |
| 6. Persist / restart | PASS | image, volume marker, dockerd survive; cached start → `docker info` 8 s |
| 7. Probes (default networking) | FAIL (expected baseline) | PASS 669, FAIL 81, WARN 45, REVIEW 11, MISSING 0 |
| 8. Host processes / listeners | recorded | one `limactl hostagent` per VM, user-level, TCP listeners on 127.0.0.1 only |
| 9. P2 knobs | recorded | only `networkOpts.hostLoopback: false` (Lima ≥ 2.3); no CIDR egress policy |

Filesystem boundary passes every check; network boundary fails on host, gateway, LAN resolvers and rebinding.

## Environment

- Lima `limactl version 2.2.1`, template `template:ubuntu-24.04`, `--vm-type=vz --cpus=4 --memory=4 --disk=30 --containerd=none --mount-type=virtiofs`, home mount replaced by exactly two mounts (`.mounts` override), all guest port auto-forwards ignored except `4096 → 127.0.0.1:47813`. Effective config: `scripts/vm-spike/lima/lima.yaml`.
- Guest: Ubuntu 24.04.5 LTS, kernel `6.8.0-142-generic` aarch64. One NIC `eth0` (`/sys/devices/pci0000:00/0000:00:01.0/virtio0`), `192.168.5.15/24`, default via `192.168.5.2`, no global IPv6 address or IPv6 default route (`-ipv6 disabled`).
- Shares: `/mnt/fx/rw` virtiofs `rw`, `/mnt/fx/ro` virtiofs `ro`; no other shared filesystem.
- Disk: `~/.lima/jlp-lima` 7.4 GB after steps 1–6; image cache `~/Library/Caches/lima` 5.0 GB.

## Steps

**2. Boot** (`logs/02.log`): `limactl create … && limactl start`; `limactl stop` 4 s; warm `limactl start` 11 s. Hostagent log: "SSH server does not seem to be running on vsock port, using usernet forwarder" — guest networking is Lima's user-mode network (gvisor-tap-vsock inside `limactl hostagent`), not vmnet.

**3. dockerd** (`logs/03.log`): Docker CE from `download.docker.com` in the guest, 23 s. `docker info`: `driver=overlayfs root=/var/lib/docker cgroup=2`; `findmnt -T /var/lib/docker` → `/dev/vda1 ext4`; `dockerd` runs as `root`.

**4–6. Workload** (`logs/04-06.log`): `limactl copy -r internal/embed/assets` then `docker build -t jailoc-base:spike .` → `sha256:e795e20306aa…`, 1.38 GB, 41 s. `docker run --privileged -v /var/run/docker.sock:… golang:1.26 go test` with `tc/redis_test.go` → `--- PASS: TestRedisPing (4.86s)`. `docker run -d --privileged docker:dind` → `dind-ok 29.9.2 overlayfs`, nested `alpine echo` OK. After `limactl stop/start`: `image-survived e795e20306aa`, marker `persisted-1793012559`, `docker` active.

## Probe baseline

Run 1 used the Go sentinel; the macOS application firewall dropped it on every non-loopback address (`S-SELFTEST-HOST-* FAIL`, accept-then-EOF/timeout), which made all host-address deny results vacuous (`results/run1-gosentinel/`). Run 2 (authoritative, `results/`) used `sentinel.py` under Apple-signed `/usr/bin/python3`, which the firewall allows; self-test then reaches every host address except the tunnel IPv6 address (filtered by the tunnel client).

Verdict totals: PASS 669, FAIL 81, WARN 45 (link-local self-test), REVIEW 11, MISSING 0.

| Check | FAILs | Cause |
|---|---|---|
| `N-HOST-TCP`, `N-HOST-UDP` | 24, 12 | every context reaches the Mac on `10.0.0.73` (LAN), `10.8.0.160` (tunnel), `192.168.100.3` (bridge); sentinel logged the payloads |
| `N-GW-*`, `N-ALIAS-*` | 6, 8 | `192.168.5.2` (`host.lima.internal`) is the host's loopback; sentinel saw `local 127.0.0.1` |
| `D-PRIV-RESOLVER-*` | 24 | private LAN resolvers `10.0.53.124/125`, `10.0.54.181` answer over UDP and TCP |
| `D-REBIND-TCP` | 4 | `10-4-68-73.sslip.io` resolves and connects to the host |
| `S-SELFTEST-HOST-*` | 3 | tunnel IPv6 address unreachable from the host itself |

Passing items worth noting: all `F-` checks — read-only virtiofs is host-enforced (`F-RO-REMOUNT`, `F-RO-FRESHMOUNT` cannot write; host listing unchanged), each share exposes only its directory (`F-SHARE-SCOPE`), guest `chown 0:0` + `chmod 4755` lands on the host owned by the host user without a root-owned setuid file (`F-HOST-SUID`), `mknod` is `EPERM`. `I-NIC` (one virtio NIC), `I-V6`, `I-VSOCK` (no host vsock listener), `L-INGRESS-*` (forward only on `127.0.0.1:47813`), `L-FAILCLOSED-DENY` (60 s, no leak; no broker kill was performed). `N-LANGW-*` and `N-META-TCP` pass, but the LAN router and metadata ports were closed, so those results only show absence of a listener.

REVIEW: `D-RESOLVER-CLASS` (guest uses `127.0.0.53` → hostagent DNS at `192.168.5.3`; containers on the bridge use `192.168.5.2`), `D-REBIND-RESOLVE`, `F-RW-SUID`, `F-RW-MKNOD`, `L-FAILCLOSED-PUBLIC` (0/60 outage ticks).

## Host processes and listeners

- One process per VM: `/opt/homebrew/bin/limactl hostagent --pidfile ~/.lima/jlp-lima/ha.pid --socket ~/.lima/jlp-lima/ha.sock …`, user `<host-user>`. It hosts the VZ VM, usernet stack, DNS, SSH forwarder and port forwards.
- TCP listeners: `127.0.0.1:49622`, `127.0.0.1:49623` (SSH), `127.0.0.1:47813` (port forward). UDP: `127.0.0.1:5353` — Lima forwarded the guest's mDNS listener automatically although TCP auto-forwards were ignored.
- Unix sockets: `ha.sock`, `ssh.sock`, `default_ep.sock`, `default_fd.sock` under `~/.lima/jlp-lima/` (mode `srwxr-xr-x`, owner user).

## P2 network-policy knobs

From [Default user-mode network](https://lima-vm.io/docs/config/network/user/):

- `networkOpts.hostLoopback: false` (Lima ≥ 2.3, `vmType: vz` only, not with `user-v2`) refuses `host.lima.internal`/`192.168.5.2`. The docs state host services listening on all interfaces remain reachable through the host's LAN address, so it does not fix `N-HOST-*` or `D-PRIV-RESOLVER-*`.
- No built-in destination CIDR/port policy in the user-mode network; `vzNAT` and `socket_vmnet` hand traffic to macOS vmnet, which needs root/pf to filter.
- `hostResolver` forwards guest DNS to the host's native resolver (split DNS included), so DNS policy must be applied there or replaced.

Conclusion for P2: Lima's filesystem side meets the spec as-is; its network path needs a jailoc-owned policy broker (e.g. a filtering gvisor-tap-vsock/usernet in front of the VZ NIC) in place of Lima's default usernet.

## Blockers and open questions

- Lima 2.3 (`hostLoopback: false`) is not yet in Homebrew's 2.2.1; not tested.
- Fail-closed under broker kill was not exercised: the broker is the hostagent itself, so killing it stops the VM.
- Peer workspace (`N-PEER-*`) and LAN sentinel (`N-LAN-*`) were not configured.
