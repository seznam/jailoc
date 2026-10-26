# P1 spike: smolvm

Spec: `vm-acceptance/1` ([vm-acceptance-spec.md](../vm-acceptance-spec.md)). Host: macOS 26.7, Apple M4 Pro, 24 GB RAM. Scripts and raw logs: `scripts/vm-spike/smolvm/` (`01-prepare.sh`, `02-install.sh`, `03-p1.sh`, `logs/`, `results-virtio/`, `results-blockpriv/`, `results-blockpriv6/`). `03-p1.sh <variant> [machine create flags]` runs one variant end to end.

Three runs:

| Run | Machine flags | Plan IPv6 mode | Purpose |
|---|---|---|---|
| `virtio` | `--net --net-backend virtio-net` | disabled | baseline, full P1 (build, Testcontainers, dind, persistence) |
| `blockpriv` | baseline + `--block-private` | disabled | native policy preview |
| `blockpriv6` | baseline + `--block-private` | enforced | same, with the IPv6 mode smolvm actually provides |

## Summary

| Step | Result | Key number |
|---|---|---|
| 1. Install | PASS | release `v1.32.3` darwin-arm64, SHA-256 checked, extracted under an isolated `HOME`; `smolvm-bin` signed with `hypervisor`, `cs.allow-jit`, `cs.disable-library-validation`; bundles its own `libkrun` and `libkrunfw` |
| 2. Boot | PASS | first `machine start` 10 s (includes pulling `docker:29-dind`), cached start < 1 s, `docker info` 1 s later |
| 3. Rootful dockerd, block-fs data-root | PASS | image's own `dockerd` (29.9.2, `overlayfs`) with `/var/lib/docker` on `/dev/vda` ext4 via disk volumes; no workaround |
| 4. Build jailoc image in guest | PASS | 42 s, no Dockerfile change |
| 5. Privileged Testcontainers + dind | PASS | `TestRedisPing` PASS (5.3 s test, 34 s incl. pulls); nested `docker:29-dind` daemon up and `nested-ok` |
| 6. Persist / restart | PASS | stop < 1 s, start < 1 s, `docker info` 1 s later; image and volume survive |
| 7. Probes, baseline | FAIL (expected) | PASS 600, FAIL 210, WARN 42, REVIEW 12, MISSING 0 |
| 7. Probes, `--block-private`, IPv6 enforced | FAIL | PASS 663, FAIL 146, WARN 42, REVIEW 13, MISSING 0; real leaks are the host's own IPv6 addresses only |
| 8. Host processes / listeners | recorded | one detached `smolvm-bin _boot-vm` per machine (VMM + virtio-fs + network stack); TCP listeners `127.0.0.1` and `::1` only |
| 9. P2 knobs | recorded | built-in `--block-private`, CIDR/UDP allow/deny, hostname allowlist, mediated egress (external loopback decider) |

smolvm meets every P1 workload step without workarounds and its read-only shares are host-enforced. `--block-private` closes all IPv4 private, host-loopback, metadata and rebinding paths. It does not treat the host's own public and tunnel IPv6 addresses as private, and the guest always has IPv6. Most remaining FAILs are a probe artifact: smolvm intercepts DNS to any destination (below).

## Environment

- Install: `results/` holds the release JSON, checksum file and tarball. The upstream `install.sh` was reviewed but not run (it uses `rm -rf` and falls back to no checksum). `HOME=/tmp/sv` (a symlink to `.spike-state/s`): with `HOME` under the repo, `machine start` fails with `socket path is 105 bytes, over the 103-byte limit` for `…/Library/Caches/smolvm/vms/<id>/agent.sock`.
- Machine: `machine create --image docker:29-dind --cpus 4 --mem 4096 --storage 30 --net --net-backend virtio-net -p 47833:4096 -v <fx>/rw:/mnt/fx/rw -v <fx>/ro:/mnt/fx/ro:ro -v <jlp>:/mnt/jlp -v /var/lib/docker -v /var/lib/containerd`.
- Guest: kernel `6.18.55` (libkrunfw), cmdline `rootfstype=virtiofs … init=/init.krun`; the smolvm agent runs from a virtio-fs root and starts the image as a container whose root is an overlay on the machine disk (`/storage/overlays/persistent-<name>/merged`). `machine exec` runs inside that container as root. No `/proc/config.gz`.
- Network (virtio-net): `eth0` `100.96.0.2/30`, gateway and resolver `100.96.0.1` (CGNAT range; `--guest-subnet` changes it), IPv6 `fd53:4d00::2/64` with a default route via `fd53:4d00::1`, plus a `dummy0` with no device. The default backend (no `--net-backend`) is TSI, which the spec rejects; it was not run.
- Disks: `storage.raw` (30 GiB sparse, `/dev/vda` ext4 for disk volumes) and `overlay.raw` (10 GiB) per machine. `.spike-state/s` 6.7 GB for three machines plus the image cache.

## Steps

**3. dockerd** (`logs/03-virtio.log`). `docker:29-dind` starts `dockerd` itself; `-v /var/lib/docker -v /var/lib/containerd` puts both on the ext4 machine disk on every start, as upstream's `docs/docker-in-machine` describes. `docker info`: `29.9.2 overlayfs /var/lib/docker`, `df` shows `/dev/vda`. The guest kernel has the netfilter pieces Docker's default iptables backend needs.

**4–5. Workload** (`results-virtio/tc.log`). `docker build` of `internal/embed/assets` succeeds in 42 s. `docker run --privileged -v /var/run/docker.sock … golang:1.26 go test` → `--- PASS: TestRedisPing (5.27s)`. Nested `docker run --privileged docker:29-dind` → `dind-ok 29.9.2 overlayfs`, inner `docker run alpine` → `nested-ok`.

**6. Persistence**. `machine stop` then `machine start`: both return in under a second, `docker info` answers 1 s later, `image-survived 78d6cd58a2fb` and the volume marker `persisted-1793015710` are present.

## Probe results

Sentinel: `sentinel.py` under `/usr/bin/python3`. Self-test reaches every host address except the tunnel IPv6 address.

### DNS interception (probe artifact)

smolvm answers UDP and TCP port 53 to **any** destination from its host resolver: `nslookup example.com 192.0.2.1` and `… 203.0.113.9` (TEST-NET, no resolver exists) both return answers, every `D-HOST-DNS`/`D-PRIV-RESOLVER-*`/`N-LANGW-DNS` answer is the same public record, and with `--block-private` `egress-denials.log` records `denied connect to 10.0.53.125:53` while the probe still gets an answer. The guest therefore never reaches those resolvers; the probe cannot tell interception from a real resolver and reports `reached`. The broker resolves through the host's resolver configuration, so internal names resolvable on the host are resolvable in the guest (information exposure, not reachability). Proposed for `vm-acceptance/2`: a TEST-NET DNS query that detects interception and downgrades these checks to `REVIEW`.

### Baseline (`results-virtio/verdict.md`)

| Check | FAILs | Cause |
|---|---|---|
| `N-HOST-TCP`, `N-HOST-UDP` | 48, 21 | every host address, IPv4 and IPv6, reaches the sentinel |
| `N-GW-TCP` | 6 | `100.96.0.1` and `fd53:4d00::1` forward to host `127.0.0.1` / `::1` |
| `N-META-TCP` | 3 | `[fd00:ec2::254]:80` accepted and held (`ambiguous-open`); IPv4 metadata is denied by smolvm's protected floor even without policy |
| `D-REBIND-TCP` | 4 | `10-4-68-73.sslip.io` connects to the host |
| `D-HOST-DNS`, `D-PRIV-RESOLVER-*`, `N-LANGW-DNS` | 24, 24, 8 | DNS interception (above) |
| `I-V6`, `N-V6-DISABLED-*` | 1, 6 | guest has IPv6 egress; plan declared it disabled |
| `L-FAILCLOSED-DENY`, `L-FAILCLOSED-SENTINEL` | 1, 60 | watch reaches the host for all 30 ticks (no policy) |
| `F-MNT-INV` | 1 | probe artifact, see below |
| `S-SELFTEST-HOST-*` | 3 | tunnel IPv6 address unreachable from the host itself |

### `--block-private`, IPv6 enforced (`results-blockpriv6/verdict.md`)

Closed compared with the baseline: all IPv4 host addresses, `N-GW-*` (host loopback), `N-META-TCP` (IPv4 and IPv6), `D-REBIND-TCP`, `N-PUB6-*` pass from the guest. Denials are logged per connection in `<data-dir>/egress-denials.log`.

| Check | FAILs | Cause |
|---|---|---|
| `N-HOST-TCP`, `N-HOST-UDP` | 18, 6 | the host's public IPv6 (`2001:db8:104:68:…`) and tunnel IPv6 (`2001:db8:248:…`, `ambiguous-open`) addresses: `--block-private` blocks address classes, not the host's own addresses |
| `L-FAILCLOSED-DENY`, `L-FAILCLOSED-SENTINEL` | 1, 60 | the watch target is the host's public IPv6 address |
| `D-HOST-DNS`, `D-PRIV-RESOLVER-*`, `N-LANGW-DNS` | 24, 23, 8 | DNS interception (above) |
| `N-PUB6-TLS`, `N-PUB6-QUIC` | 1, 1 | `container-bridge` only: the guest `dockerd` default bridge has no IPv6 (guest configuration, not the boundary) |
| `F-MNT-INV` | 1 | probe artifact, see below |
| `S-SELFTEST-HOST-*` | 3 | tunnel IPv6 address unreachable from the host itself |

The `blockpriv` run (IPv6 declared disabled) gives the same picture plus the six `N-V6-DISABLED-*` and the `I-V6` FAIL.

### Filesystem and interfaces (all runs)

- Every `F-RO-*` check passes, including `F-RO-REMOUNT` and `F-RO-FRESHMOUNT`: `:ro` is enforced by smolvm's host-side virtio-fs server. `F-SHARE-SCOPE`, `F-OUTSIDE-*`, `F-RW-SYMLINK-*`, `F-RW-DOTDOT`, `F-SOCK` pass. `F-RW-SUID` and `F-RW-MKNOD` succeed in the guest, but `F-HOST-SUID` and `F-HOST-MKNOD` pass: the host sees files owned by the host user and no device node.
- `F-MNT-INV` FAIL is a probe artifact: the container sees the three declared shares (`smolvm0`, `smolvm1`, `smolvm2`) once at their declared paths and again under the agent's overlay path `/storage/overlays/persistent-<name>/merged/mnt/…`. No undeclared virtio-fs tag appears. The agent's own root is a virtio-fs share of the host `agent-rootfs` directory, which the container does not see.
- `I-NIC` and `I-ROUTE-V4` pass (one virtio NIC). `I-VSOCK` REVIEW: guest CID 2 port 5001 is open, which is smolvm's `WORKLOAD_LOGS` port (`crates/smolvm-protocol`), a guest-to-host log stream.
- `L-INGRESS-*` pass: `-p 47833:4096` listens on `127.0.0.1` and `[::1]` only.

## Host processes and listeners

- One process per running machine, user `<host-user>`, parent `launchd`: `smolvm-bin _boot-vm <cache>/vms/<id>/boot-config.json`. It holds the VMM, the virtio-fs servers and the virtio-net network stack, so the broker and the VMM share one host authority. The CLI exits after `start`.
- Listeners: TCP `127.0.0.1:47833` and `[::1]:47833` (port forward). UDP: one host socket per guest flow (NAT).
- Control surface: Unix sockets `agent.sock`, `control.sock`, `ports.sock`, `agent.logs`, `agent.ready` in `~/Library/Caches/smolvm/vms/<id>/` (mode `0755` directory, user-owned), plus `smolvm serve` (HTTP API) when used. None is reachable from the guest by the probes.
- Entitlements: `com.apple.security.hypervisor`, `cs.allow-jit`, `cs.disable-library-validation`. No App Sandbox.

## P2 network-policy knobs

From `machine create --help`, `docs/mediated-egress.md` and `docs/security-model.md` (upstream `main` at `fb0d734`):

- `--block-private` / `--allow-private <CIDR>`: blocks private, internal, link-local, CGNAT, metadata and host loopback; public stays open. Measured above.
- `--deny-cidr`, `--allow-cidr`, `--allow-udp CIDR[:PORT[-END]]`, `--allow-host` (resolved at VM start), `--allow-host-pattern`. Deny rules are checked first. `--deny-cidr` with the host's own addresses would likely close the remaining IPv6 FAILs; P2 has to verify it, and the address list changes when the host roams.
- Mediated egress (`--mediated-egress --egress-interceptor 127.0.0.1:PORT`): every guest TCP flow is decided by an external loopback decider (allow, deny, redirect) using destination IP, port and first bytes; decider failure denies the flow. UDP and ICMP are denied unless a static rule allows them. This is a natural place for a jailoc-owned broker that enforces the spec's hard-deny set computed on the host. Requires virtio-net.
- `--guest-subnet` moves the guest off `100.96.0.0/30` (CGNAT, collides with Tailscale).
- `--network <name>` joins machines into an inter-VM network; not used. Peer isolation between separate machines was not tested.

## Blockers and open questions

- The host's own non-private addresses (public and tunnel IPv6) are reachable even with `--block-private`; the hard-deny set needs `--deny-cidr` or a mediated decider and must follow address changes.
- IPv6 is always on with virtio-net (ULA guest address, NAT66 egress). No flag to disable it was found; jailoc would run in `enforced` mode and rely on the policy for IPv6 deny ranges.
- DNS interception answers queries to any address from the host resolver, so internal names visible on the host resolve in the guest. Acceptable as "DNS via the controlled path" if resolution is not used for authorization, but it needs a decision in the ADR.
- `HOME`-relative socket paths must stay under 103 bytes on macOS; jailoc must place smolvm state on a short path.
- Fail-closed behavior on broker failure was not exercised: the network stack lives in the VMM process, so killing it kills the VM. Mediated-egress decider failure denies flows by design; not tested.
- Linux/KVM not tested (macOS only). Upstream `docs/docker-in-machine` reports Docker in a machine on Linux aarch64 with 1 GiB guests on a Lima host.
- Peer workspace and LAN sentinel not configured.
