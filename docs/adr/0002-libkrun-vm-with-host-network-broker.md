---
status: accepted
---

# Workspaces run in go-microvm VMs whose only network path is a jailoc broker

Each workspace is one Linux VM booted through [go-microvm](https://github.com/stacklok/go-microvm) (libkrun: Hypervisor.framework on macOS, KVM on Linux), embedded as a Go library. The VM's only network interface connects to `jailoc-netbroker`, a host process running a gVisor userspace stack that checks every guest flow against the workspace policy before it opens a host socket and fails closed. jailoc starts the broker, boots the VM once the broker is ready, and stops one when the other exits.

The agent gets root, a rootful `dockerd` and privileged containers inside the guest, so nothing enforced in the guest counts as a boundary. What remains are the hypervisor, file sharing enforced by the VMM, and networking controlled from the host. Every candidate was measured against the same executable contract (`jailoc-vmprobe`, `vm-acceptance/2`) from four positions inside the workspace: guest root, guest user, a bridged container and a privileged host-network container. Only go-microvm with the broker as its sole NIC passed every deny check from all four, and stayed closed on invalid policy, a killed broker, a dead broker at boot and a cached start. Research and probe runs: [#244](https://github.com/seznam/jailoc/issues/244), [#252](https://github.com/seznam/jailoc/pull/252).

## Considered options

- **Lima.** Stable and passes the filesystem checks, but always adds its own user-mode NIC next to any socket network and has no egress policy, so a broker cannot be the only path.
- **smolvm.** Passes the filesystem and Docker checks and has a built-in policy with an external decider, but its network stack cannot be replaced; jailoc would depend on smolvm's policy semantics and DNS interception.
- **Firecracker.** Linux only, no shared file system, policy only through host nftables on a TAP device.
- **Backend firewalls instead of a broker** (go-microvm `WithFirewallRules`, smolvm `--block-private`). Each covers part of the contract (IPv4 only, or host IPv6 addresses leak) and differs per backend. One broker keeps the policy, its tests and its logs identical across hosts.
- **Host kernel firewall (nftables on a TAP).** Linux only, and fails open when the table does not load.

## Consequences

- jailoc owns a network stack: DHCP, filtered DNS, TCP/UDP relaying and port forwarding are jailoc code with their own tests.
- The broker pins the gVisor version that go-microvm's network stack uses. Denied UDP is dropped silently instead of answered with ICMP unreachable.
- libkrun does not reconnect the NIC socket, so a broker crash takes the workspace offline; the supervisor stops the VM with it.
- jailoc downloads a pinned, checksum-verified runtime (runner, libkrun, libkrunfw) instead of linking libkrun, so the jailoc binary has no C dependencies.
- Hosts are limited to what libkrun supports: darwin/arm64 and linux/amd64 with KVM.
