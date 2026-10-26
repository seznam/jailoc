# VM backend decision record

Status: proposed. The macOS evidence is complete; the Linux half and performance measurements are open.
Context: [seznam/jailoc#244](https://github.com/seznam/jailoc/issues/244), acceptance contract [vm-acceptance-spec.md](vm-acceptance-spec.md).

## Context

jailoc runs untrusted coding agents. The goal of #244 is a backend in which each workspace is one Linux VM with guest root, rootful `dockerd` and privileged containers, without host Docker, on Linux x86_64/arm64 and macOS arm64. The security boundaries are the hypervisor, host-enforced file sharing and host-controlled networking; nothing enforced inside the guest counts.

Every candidate was measured against the same executable contract (`jailoc-vmprobe`, `vm-acceptance/2`) from four positions inside the workspace: guest root, guest user, a bridged container and a privileged host-network container.

## Decision

1. **VM layer: [go-microvm](https://github.com/stacklok/go-microvm)** (libkrun, Hypervisor.framework on macOS, KVM on Linux), embedded as a Go library.
2. **Network: `jailoc-netbroker` is the only network path.** go-microvm attaches the guest NIC to the broker's unix socket (`WithNetProvider`); the broker is a gVisor userspace stack that decides every guest flow against the deny ranges, host addresses and workspace exceptions before it opens a host socket, and fails closed on invalid policy or crash. Candidate-specific firewalls are not used. See [p2-broker.md](p2-broker.md).
3. **Supervision:** jailoc starts the broker, waits for its `ready` signal, boots the VM, waits for the NIC to connect, and stops one side when the other exits (`internal/brokerproc`).
4. **Storage:** Docker state lives on a virtio-blk disk image per workspace, formatted in the guest on first boot. This needs `WithDisks` from [stacklok/go-microvm#128](https://github.com/stacklok/go-microvm/pull/128). Project paths are virtio-fs shares; read-only shares are enforced by the VMM.
5. **IPv6:** `disabled` in the first backend; the guest has no global IPv6 address or route.
6. **macOS packaging:** the broker ships as a signed helper binary. It needs the macOS network permission for local-subnet destinations to reach allowed private exceptions; without it those exceptions fail closed.

## Evidence

| Area | Result | Source |
|---|---|---|
| Rootful `dockerd`, image build, Testcontainers, persistence in go-microvm | PASS | [candidates/go-microvm.md](candidates/go-microvm.md) |
| Read-only shares, share scope, ownership mapping | PASS (VMM-enforced) | same |
| Broker as only NIC, all deny checks, four contexts | PASS, no sentinel hit | [p2-broker.md](p2-broker.md) |
| Invalid policy, broker kill, dead broker at boot, cached start | fail-closed | same |
| Two concurrent workspaces, peer isolation, supervision | PASS | same, "Supervision and multiple workspaces" |
| Virtio-blk data disk, read-only disk enforced by the VMM | PASS on macOS | stacklok/go-microvm#128 |
| Debian 13 with systemd as PID 1, networkd/resolved through the broker, apt, Docker CE on the data disk, two boots | PASS, no failed units | [debian-systemd.md](debian-systemd.md) |

## Alternatives

- **Lima:** stable and passes the filesystem checks, but always adds its own user-mode NIC next to any socket network and offers no egress policy, so a broker cannot be the only path. Kept as the baseline in [candidates/lima.md](candidates/lima.md).
- **smolvm:** passes P1 cleanly and has a built-in policy with an external decider, but its network stack cannot be replaced; jailoc would depend on smolvm's policy semantics and its DNS interception. [candidates/smolvm.md](candidates/smolvm.md).
- **Firecracker:** Linux only, no file sharing, policy only through host nftables on a TAP device. Deferred. [candidates/firecracker.md](candidates/firecracker.md).
- **Candidate firewalls instead of a broker** (go-microvm `WithFirewallRules`, smolvm `--block-private`): each covers part of the contract (IPv4 only, or leaks host IPv6 addresses) and differs per backend. One jailoc broker keeps the policy, its tests and its logs identical across backends.
- **Host kernel firewall (nftables on a TAP):** works on Linux, but not on macOS, and fails open when the table does not load.

## Consequences

- jailoc owns a network stack: DHCP, DNS forwarding, TCP/UDP relaying and ingress forwarding are jailoc code and need their own tests and maintenance.
- The broker pins the gVisor version that go-microvm's network stack uses. Denied UDP is dropped silently instead of answered with ICMP unreachable.
- A broker crash takes the workspace offline until jailoc restarts it, because libkrun does not reconnect the NIC socket.
- jailoc depends on go-microvm#128 until it is merged; a fork pin is used meanwhile.
- On macOS the broker is a signed helper with a user-granted network permission.

## Open items

- **Linux (deferred, no KVM host available):** run the same stack on Linux/KVM (x86_64 and arm64) and repeat the probe; measure cached start, memory and 1–3 concurrent workspaces (P4). Needs a host with `/dev/kvm`.
- **Root filesystem:** go-microvm's root filesystem is a virtio-fs share, which fails `F-MNT-INV`. Decide between accepting it (it holds the OCI image, not host files) or a block-device root.
- **Nested Docker:** go-microvm's guest kernel lacks the iptables modules nested `docker:dind` needs; `dockerd` in the guest runs with the nftables backend. Testcontainers works; dind-in-dind workloads do not.
- **Allowed private exceptions:** the allow path is covered by unit tests only; repeat end to end with the signed helper.
- **Backend integration:** `jailoc up --backend vm`, mapping `allowed_hosts` and `allowed_networks` to broker exceptions and `paths` to shares.
- **systemd guests:** libkrun runs the workload as PID 1 only when `KRUN_INIT_PID1=1` reaches its init, which go-microvm's runner cannot pass today (needs `krun_set_env`); upstream an init-environment option.
- **Disk image shrink:** libkrun's block layer (imago < 0.2.5) truncates the image when a discard or write-zeroes request reaches its end, so `mkfs` shrinks a fresh data disk and the next mount fails. Regrow the image to its configured size before every boot until libkrun ships imago 0.2.5.
