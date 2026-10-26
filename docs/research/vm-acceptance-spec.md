# VM backend acceptance specification

Spec version: `vm-acceptance/2` (frozen). Source decision: [seznam/jailoc#244](https://github.com/seznam/jailoc/issues/244#issuecomment-6349301366). Changes from `vm-acceptance/1` are listed under [Revisions](#revisions).

This document is the single pass/fail contract for every VM backend candidate (see [Candidates](#candidates)). Every candidate runs the same probe binary, `jailoc-vmprobe`, with the same plan format and is judged by the same verdict rules. A candidate that cannot run a check counts as failing that check.

Changing a rule in this document creates the next version. Results produced under different spec versions are not comparable.

## Candidates

| Candidate | Round | Hosts |
|---|---|---|
| [Lima](https://github.com/lima-vm/lima) | first (baseline) | Linux, macOS |
| [Stacklok go-microvm](https://github.com/stacklok/go-microvm) | first | Linux, macOS |
| [smolvm](https://github.com/smol-machines/smolvm) | first | Linux, macOS |
| [Firecracker](https://github.com/firecracker-microvm/firecracker) | deferred (Linux only, no file sharing) | Linux (KVM, x86_64 and aarch64) |
| Hypeman, Gondolin, microsandbox, direct libkrun/vfkit/QEMU | secondary | varies |

Network policy is enforced by a jailoc-owned broker in front of the candidate's single guest NIC, not by candidate-specific policy features. Candidates are evaluated for how cleanly they hand that one network path to the broker.

P1 spike reports: [Lima](candidates/lima.md), [go-microvm](candidates/go-microvm.md), [smolvm](candidates/smolvm.md), [Firecracker](candidates/firecracker.md). Spike scripts: `scripts/vm-spike/`. P2 broker prototype and run: [p2-broker.md](p2-broker.md).

### Firecracker

Firecracker runs only on Linux/KVM. It can satisfy the Linux half of the platform goal, but a macOS arm64 backend must be paired with it, and both halves must pass this spec independently. The VMM exposes only virtio-net, virtio-block, virtio-vsock, virtio-pmem, entropy and balloon devices, which affects these checks:

- **Filesystem (`F-`)**: Firecracker has no host directory sharing (no virtio-fs or 9p). Read-write project paths need a host-side file server reached through vsock or the network, and that server is the boundary the `F-` checks measure. Read-only shares can be read-only block or pmem images. Whatever mechanism is chosen appears in `F-MNT-INV` and must be declared in the plan.
- **Network (`N-`, `D-`, `I-NIC`)**: the guest NIC is a host TAP device. A bare TAP is the rejected "unfiltered TAP" path, so the candidate must put the policy broker (nftables on the TAP, or a userspace stack) in front of it, and the whole run tests that broker. Creating TAP devices and filtering them needs `CAP_NET_ADMIN` on the host, delivered through the managed helper.
- **Metadata (`N-META-TCP`)**: the guest-facing MMDS answers on `169.254.169.254` when configured, so it must stay unconfigured or `N-META-TCP` fails.
- **vsock (`I-VSOCK`)**: guest connections to CID 2 go to host Unix sockets named `<uds_path>_<port>`. Every port with a host listener is reported for review.
- **VMM authority**: the [jailer](https://github.com/firecracker-microvm/firecracker/blob/main/docs/jailer.md) chroots and namespaces the VMM and drops privileges. Its configuration and the API socket location are part of the manual evidence.
- **Rootful guest `dockerd`**: the guest kernel is supplied by the candidate and must include overlayfs, netfilter and bridge support for nested Docker.

## Threat model

- One Linux VM per workspace is one untrusted domain. Guest root, rootful `dockerd`, privileged containers, host-network containers and malicious Dockerfile `RUN` steps are all **inside** the domain and assumed hostile.
- Security boundaries are only: the VMM/hypervisor, host-enforced filesystem sharing, and host-controlled networking. Anything enforced by the guest kernel (iptables in the guest, `ro` mount flags, guest DNS config) is **not** a boundary and earns no credit.
- Every network check runs from four contexts, because each one is a distinct attacker position inside the same domain:

| Context | Where the probe runs |
|---|---|
| `guest-root` | VM guest as uid 0 |
| `guest-user` | VM guest as an unprivileged user |
| `container-bridge` | container on the guest `dockerd` default bridge |
| `container-hostnet-privileged` | `docker run --privileged --network=host` in the guest |

Filesystem checks run from `guest-root` (they need `CAP_SYS_ADMIN` to attempt remounts and fresh mounts).

## Status and observation semantics

Each check result has one status:

| Status | Meaning | Verdict effect |
|---|---|---|
| `PASS` | Requirement met | — |
| `FAIL` | Requirement violated | Candidate fails |
| `MISSING` | Required evidence absent (context, self-test, ingress or watch report not supplied) | Candidate fails |
| `WARN` | Requirement met but operability is degraded (e.g. slow IPv6 denial) | Recorded, does not fail |
| `REVIEW` | Needs a human decision recorded in the evaluation (e.g. open vsock ports, guest resolver class) | Must be resolved in writing before the ADR |
| `SKIP` | Check not applicable in this context, or a deny probe whose target is assigned inside the workspace domain (see [Verdict rules](#verdict-rules)) | — |

Network probes classify every attempt into one observation:

| Observation | Definition |
|---|---|
| `blocked` | Connection refused, unreachable, address not available, or timeout |
| `proxy-closed` | Connection accepted, then closed or reset with no payload received |
| `reached` | Payload or banner received from the destination |
| `ambiguous-open` | Connection accepted and held open with no data until timeout |

A TCP handshake alone is **not** evidence of reachability: userspace network stacks (gvproxy, passt, slirp, TSI) accept locally and then reset when the broker denies the destination. Deny checks pass only on `blocked` or `proxy-closed`. Allow checks pass only on `reached`. `ambiguous-open` fails deny checks.

Every probe carries the payload `JLP1 <nonce> <key> <context>`. Sentinels on the host and LAN log every received payload. A logged payload for a deny-expected key fails that check even if the guest-side observation looked blocked; this catches one-way leaks (UDP, accept-then-forward).

## Network policy

Exactly one network path leaves the guest, and it is policy-controlled on the host. The host evaluates the real destination address before it opens a host socket or forwards a packet.

### Public egress

Any destination not in the deny ranges below is public. All TCP and UDP ports to public destinations are allowed. ICMP may be allowed or denied. All other IP protocols are denied.

### Deny ranges

IPv4:

| Range | Name |
|---|---|
| `0.0.0.0/8` | this-network |
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` | rfc1918 |
| `100.64.0.0/10` | cgnat |
| `127.0.0.0/8` | loopback |
| `169.254.0.0/16` | link-local |
| `192.0.0.0/24` | ietf-protocol-assignments |
| `192.0.2.0/24`, `198.51.100.0/24`, `203.0.113.0/24` | test-net-1/2/3 |
| `192.88.99.0/24` | 6to4-relay-anycast |
| `198.18.0.0/15` | benchmarking |
| `224.0.0.0/4` | multicast |
| `240.0.0.0/4` (includes `255.255.255.255`) | reserved-and-broadcast |

IPv6:

| Range | Name |
|---|---|
| `::/128` | unspecified |
| `::1/128` | loopback |
| `::ffff:0:0/96` | IPv4-mapped — evaluated by the embedded IPv4 address |
| `64:ff9b::/96` | NAT64 — evaluated by the embedded IPv4 address |
| `64:ff9b:1::/48` | local-nat64 |
| `100::/64` | discard-only |
| `2001::/23` (includes Teredo `2001::/32`) | ietf-protocol-assignments |
| `2001:db8::/32` | documentation |
| `2002::/16` | 6to4 |
| `fc00::/7` | unique-local |
| `fe80::/10` | link-local |
| `fec0::/10` | site-local (deprecated) |
| `ff00::/8` | multicast |

### Hard-deny destinations

No exception can open these, whatever their address class:

- loopback of the host,
- every address assigned to any host interface, including public addresses and tunnel addresses,
- the broker/gateway address, except DNS on port 53 and DHCP,
- peer workspace VMs,
- cloud metadata endpoints `169.254.169.254`, `169.254.170.2`, `fd00:ec2::254`,
- host aliases published by the runtime (`host.docker.internal`, `host.lima.internal`, `host.containers.internal`, and similar), whatever address they resolve to.

### Exceptions

An exception is `{cidr, proto (tcp|udp|any), ports}`; an empty port list means all ports. Exceptions only open deny ranges, never hard-deny destinations.

- `allowed_networks` maps to an exception per CIDR, all ports, both protocols.
- `allowed_hosts` is resolved **on the host at workspace start** into frozen IP exceptions (private results only; public results need no exception). The mapping name → IPs is recorded. Guest DNS answers never create or widen an exception.

### DNS

- The guest resolver is the broker. UDP and TCP DNS both work.
- Direct DNS and DoH/DoT to public resolvers are allowed (they are public egress).
- Private resolvers of the host or LAN are denied unless an exception covers them.
- A name resolving to a denied address (rebinding, e.g. `10-0-0-1.sslip.io`) may resolve, but the connection to the resulting address is denied.
- `filtered_dns` parity is optional for this spec.
- A runtime may intercept DNS sent to any address and answer it itself. The probe detects this by querying `192.0.2.53` (TEST-NET-1, never a resolver). When intercepted, a DNS answer from a denied destination is `REVIEW` instead of `FAIL`, because it does not prove the destination is reachable; whether the interceptor forwards to private resolvers is then judged from the broker configuration.

### IPv6

The candidate declares one mode in the plan:

- `enforced`: IPv6 egress works for public destinations and the IPv6 deny ranges are enforced exactly like IPv4.
- `disabled`: the guest has no global IPv6 address and no IPv6 default route; IPv6 connects fail in every context, including host-network privileged containers. Denial slower than 1 s is `WARN`.

### Interfaces and side channels

- Exactly one network device in the guest (`/sys/class/net/*/device`), carrying all default routes.
- No libkrun TSI fallback, no secondary NIC, no unfiltered TAP, no alternate NAT.
- vsock ports reachable from the guest on the host (CID 2) are listed and each one requires a written justification (`REVIEW`). A guest-reachable management service is a failure.

### Fail-closed behavior

The policy is active before any workload starts. While the broker is killed, restarted, given invalid policy, or during a cached start, no deny-expected destination is reached at any moment. Public connectivity may drop during these events.

## Filesystem policy

- Project paths are shared read-write.
- Mutable OpenCode config is copied into workspace-private state, not shared.
- Every other shared path is read-only, **enforced on the host**. Guest root must be unable to write it through any route: direct write, chmod/chown/utimes/xattr, rename/unlink, `mount -o remount,rw`, or a fresh mount of the same share source.
- A share exposes only its export. A fresh mount of the share source from the guest must not reveal host files outside the export.
- Files created by guest root on a read-write share land on the host owned by the host user, without setuid bits and without device nodes.
- Symlinks and `..` created in the guest do not leak host paths outside the export when followed on the host side of the share.
- Host files outside exports, sibling workspace state, host Docker sockets, credentials and lifecycle/control sockets are absent from the guest. The only socket allowed on a share is an explicitly declared one (SSH agent).
- Every shared filesystem mounted in the guest (`virtiofs`, `9p`, `fuse*`, `nfs`, `cifs`, and similar) is one of the declared exports.
- Secrets delivered on purpose are visible to guest root; this is accepted.

## Ingress and control

- The OpenCode port is published on host `127.0.0.1` only. It is unreachable on every non-loopback host address.
- No host-side listener created by the candidate binds a non-loopback address.
- VMM and broker processes run with only the host authority they need; lifecycle control is separate from network and filesystem authority.

## Procedure

All commands are `jailoc-vmprobe <subcommand>`. Build the guest binary with `CGO_ENABLED=0 GOOS=linux GOARCH=<arch> go build ./cmd/jailoc-vmprobe`.

1. **Plan** (host). Creates fixtures and the shared nonce.

    ```sh
    jailoc-vmprobe plan -candidate lima -dir "$FIX" -guest-prefix /mnt/fx \
      -ipv6 disabled -lan-sentinel 10.0.0.20 -peer 192.168.5.15 \
      -exception 10.0.0.20/32,tcp,47801 -out plan.json
    ```

    `$FIX/rw` and `$FIX/ro` are exported to the guest at the guest prefix with the matching mode. `$FIX/outside` is never exported.

2. **Sentinels.** On the host, and on a second LAN machine:

    ```sh
    jailoc-vmprobe sentinel -plan plan.json -name host -log hits-host.jsonl
    ```

    On macOS the application firewall silently drops unsigned binaries on non-loopback addresses, which makes every host-address deny check vacuous. Run the equivalent Python sentinel under the Apple-signed interpreter, which the firewall allows by default:
    `/usr/bin/python3 cmd/jailoc-vmprobe/sentinel.py host hits-host.jsonl 47801 47802`.

3. **Self-test** (host). Proves the sentinels are reachable, so deny results are not vacuous. Addresses on interfaces that are down and link-local addresses the host cannot dial are `WARN`, and deny results for them are not conclusive:
   `jailoc-vmprobe selftest -plan plan.json -out r-selftest.json`.

4. **Guest probes**, one per context:

    ```sh
    jailoc-vmprobe guest -plan plan.json -context guest-root -out r-guest-root.json
    jailoc-vmprobe guest -plan plan.json -context guest-user -out r-guest-user.json
    docker run --rm -v ...:/p <img> /p/jailoc-vmprobe guest -context container-bridge ...
    docker run --rm --privileged --network=host -v ...:/p <img> \
      /p/jailoc-vmprobe guest -context container-hostnet-privileged ...
    ```

    The default per-probe timeout is 3 s. A guest too slow to complete the public positive controls in 3 s may run with a longer `-timeout`; the value is recorded in the evaluation.

5. **Fail-closed watch.** Run in the guest while the operator kills the broker, restarts it, loads an invalid policy, and stops/starts the VM from cache:
   `jailoc-vmprobe watch -plan plan.json -context guest-root -duration 5m -timeline tl.jsonl -out r-watch.json`.

6. **Ingress** (host) against the published OpenCode port:
   `jailoc-vmprobe ingress -plan plan.json -port 4096 -out r-ingress.json`.

7. **Verdict** (host):

    ```sh
    jailoc-vmprobe verdict -plan plan.json -hits hits-host.jsonl \
      -hits hits-lan.jsonl -out verdict.md r-*.json
    ```

    Exit status 0 means PASS, 1 means FAIL, 2 means the inputs were invalid.

### Manual evidence

These are recorded in the evaluation next to `verdict.md`, because they cannot be observed from inside the guest:

- host listener diff (`lsof -nP -iTCP -sTCP:LISTEN`, `ss -lntup`) before and after workspace start,
- VMM and broker process user, capabilities/entitlements, and open file descriptors,
- the exact broker kill/restart/invalid-policy/cached-start sequence executed during the watch, with timestamps.

## Check catalog

Network (`N-`), all four contexts:

| ID | Expect | Requirement |
|---|---|---|
| `N-PUB-TLS-IP`, `N-PUB-TLS-NAME` | allow | TLS to `1.1.1.1:443` and `example.com:443` |
| `N-PUB-DNS-UDP`, `N-PUB-DNS-TCP` | allow | DNS to `1.1.1.1:53` |
| `N-PUB-QUIC` | allow | QUIC version negotiation on public UDP 443 |
| `N-PUB6-TLS`, `N-PUB6-QUIC` | allow | Same over IPv6 (mode `enforced`) |
| `N-V6-DISABLED-TCP`, `N-V6-DISABLED-UDP` | deny | IPv6 fails fast (mode `disabled`) |
| `N-HOST-TCP`, `N-HOST-UDP` | deny | Every host interface address, primary and alternate port |
| `N-GW-TCP`, `N-GW-UDP` | deny | Guest default gateways |
| `N-ALIAS-TCP`, `N-ALIAS-UDP` | deny | Runtime host aliases |
| `N-LAN-TCP`, `N-LAN-UDP` | per exception | LAN sentinels; only excepted proto/port reached |
| `N-PEER-TCP`, `N-PEER-UDP` | deny | Peer workspace addresses |
| `N-META-TCP` | deny | Metadata endpoints |
| `N-LANGW-TCP`, `N-LANGW-DNS` | deny | LAN router (first address of each private host prefix) |

DNS (`D-`), all four contexts:

| ID | Expect | Requirement |
|---|---|---|
| `D-RESOLVER-UDP`, `D-RESOLVER-TCP` | allow | Guest resolver answers over UDP and TCP |
| `D-RESOLVER-CLASS` | review | Address class of the guest resolver |
| `D-PRIV-RESOLVER-UDP`, `D-PRIV-RESOLVER-TCP` | deny | Host private resolvers |
| `D-HOST-DNS` | deny | DNS on host interface addresses |
| `D-REBIND-RESOLVE` | review | Rebinding name resolution recorded |
| `D-REBIND-TCP` | deny | Connect to rebinding name fails |
| `D-DNS-INTERCEPT-UDP`, `D-DNS-INTERCEPT-TCP` | review if answered | DNS to `192.0.2.53` detects runtime DNS interception |

Interfaces (`I-`), `guest-root` only:

| ID | Expect | Requirement |
|---|---|---|
| `I-NIC` | — | Exactly one physical network device |
| `I-ROUTE-V4` | — | All IPv4 default routes use that device |
| `I-V6` | — | Matches declared IPv6 mode |
| `I-VSOCK` | review | Host vsock ports reachable from the guest |

Filesystem (`F-`), `guest-root` in the guest plus host-side verification:

| ID | Requirement |
|---|---|
| `F-MNT-INV`, `F-MNT-DECLARED` | Shared mounts equal declared exports |
| `F-OUTSIDE-DIRECT`, `F-OUTSIDE-WALK` | Outside canary not reachable by path or by walking shares |
| `F-RW-WRITE` | Writes to read-write export succeed |
| `F-RW-SUID`, `F-RW-MKNOD` | Root-owned setuid file and device node attempts (judged on host) |
| `F-RW-SYMLINK-ABS`, `F-RW-SYMLINK-REL`, `F-RW-DOTDOT` | No escape through links or `..` |
| `F-RO-READ` | Read-only export readable |
| `F-RO-CREATE`, `-WRITE`, `-TRUNCATE`, `-CHMOD`, `-CHOWN`, `-UTIMES`, `-XATTR`, `-MKDIR`, `-SYMLINK`, `-RENAME`, `-UNLINK` | Every mutation fails |
| `F-RO-REMOUNT`, `F-RO-FRESHMOUNT` | Remount and fresh rw mount cannot write |
| `F-SHARE-SCOPE` | Fresh mount of share source shows only the export |
| `F-SOCK` | No undeclared sockets on shares |
| `F-HOST-RO-CANARY`, `F-HOST-RO-LISTING` | Host read-only fixture unchanged |
| `F-HOST-OUTSIDE`, `F-HOST-OUTSIDE-LISTING` | Host outside fixture unchanged |
| `F-HOST-RW-WRITE` | Guest write visible on host |
| `F-HOST-SUID`, `F-HOST-MKNOD` | No root-owned or foreign setuid file, no device node on host |

Lifecycle (`L-`), self-test (`S-`) and coverage (`C-`):

| ID | Requirement |
|---|---|
| `L-FAILCLOSED-DENY`, `L-FAILCLOSED-SENTINEL` | No deny-expected destination reached during the watch |
| `L-FAILCLOSED-PUBLIC` | Public outage windows recorded (review) |
| `L-INGRESS-LOOPBACK`, `L-INGRESS-EXTERNAL` | Published port on `127.0.0.1` only |
| `S-SELFTEST-HOST-*`, `S-SELFTEST-LAN-*` | Sentinels reachable from the host |
| `S-UNATTRIBUTED`, `S-UNKNOWN-KEY` | Sentinel traffic not attributable to a probe (review) |
| `C-COVERAGE` | All four guest contexts, self-test, ingress and watch reports present |

## Verdict rules

1. All reports and sentinel hits must carry the plan nonce; reports with a different nonce are rejected.
2. Any `FAIL` or `MISSING` fails the candidate.
3. Every `REVIEW` item is resolved in writing in the candidate evaluation.
4. Candidate configuration may be tuned and re-run, but the spec, plan options other than addresses, and probe binary may not be changed per candidate.
5. Every guest report records the addresses assigned in its network namespace. A deny probe that fails without a sentinel hit, and whose target is one of those addresses in any guest context, becomes `SKIP`: it reached the guest or one of its containers, not the host. This happens when host and guest use the same private range, e.g. both run Docker's `172.17.0.0/16` bridge. A sentinel hit always fails the probe, because sentinels only run outside the domain.

## Revisions

| Version | Changes |
|---|---|
| `vm-acceptance/1` | Initial contract. |
| `vm-acceptance/2` | DNS interception detection (`D-DNS-INTERCEPT-*`, interception turns DNS-to-denied answers into `REVIEW`); deny probes to addresses inside the workspace domain become `SKIP` unless a sentinel saw them; self-test `WARN` for host interfaces that are down; `D-HOST-DNS` probes IPv6 link-local host addresses per guest interface; Python sentinel for macOS hosts; guest `-timeout` may be raised for slow guests; Firecracker deferred; network policy comes from a jailoc-owned broker. |
