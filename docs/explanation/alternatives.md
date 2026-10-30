# Why Not Something Else?

jailoc runs each workspace in its own libkrun micro-VM, driven through go-microvm, with a host-side network broker. Several other isolation tools exist. This page explains why jailoc does not build on them.

!!! note "Research date"
    The statements about third-party tools reflect their documentation and issue trackers as of 2026-10-30. These projects change quickly; check the linked sources for their current state.

The requirements behind the choice:

- **Two hosts**: macOS on Apple silicon and Linux on x86-64, with the same behaviour on both.
- **A separate kernel**: the agent gets root and `sudo` in the guest, so the boundary cannot be the host kernel.
- **Live project directories**: the agent edits the real files on the host, so the sandbox needs a shared filesystem, not a copied image.
- **Host-enforced network policy**: egress rules must live outside the guest, where root in the guest cannot change them.
- **Ordinary Linux userland**: package managers, language toolchains and a nested Docker daemon (`enable_docker`) must work unmodified.
- **One binary**: jailoc is a Go CLI; the sandbox has to be embeddable as a library, not a separate service to install and operate.

## Plain containers (Docker, Podman, Dev Containers)

jailoc v1 ran workspaces as Docker containers. A container shares the host kernel, so a kernel bug or a misconfigured capability, mount or socket ends on the host. Giving the agent root and its own Docker daemon inside a container means either a privileged container or a mounted host Docker socket, and both hand the host to the agent. Network rules applied with iptables live in a kernel the container shares.

On macOS, Docker already runs inside a Linux VM, but that VM is shared by every container, so one escape reaches all workspaces and the host's mounted directories.

jailoc still uses host Docker to build images. Workspaces never run on it. See [Overview](overview.md#why-a-vm).

## gVisor

gVisor puts a userspace kernel (the Sentry) between the container and the host kernel. It is a strong boundary, but:

- It runs on Linux only. On macOS it would have to run inside another VM, which is the thing jailoc already does.
- It implements the Linux syscall surface itself, and some software relies on syscalls, `/proc` details or kernel features it handles differently. Running Docker inside gVisor is limited.
- Its network stack is the right tool for a policy proxy, but a sandbox still needs a place to enforce policy that the guest cannot reach.

jailoc does use part of gVisor: the network broker runs gVisor's userspace TCP/IP stack on the host. See [Network Isolation](network-isolation.md).

## Kata Containers

Kata runs each container in a lightweight VM, which matches jailoc's boundary. It is built for container orchestration: it plugs into containerd or CRI-O as a runtime, needs KVM (or another Linux hypervisor), and is configured through the container runtime stack. It does not run on macOS, and using it would mean installing and operating containerd plus Kata on every developer machine instead of shipping one binary.

## Firecracker

Firecracker is a minimal KVM-based VMM. It is Linux-only and has no shared-filesystem device: the maintainers rejected 9p and virtio-fs over attack surface and snapshot complexity, and virtio-fs is not on the roadmap ([firecracker#1180](https://github.com/firecracker-microvm/firecracker/issues/1180), [firecracker#1351](https://github.com/firecracker-microvm/firecracker/pull/1351)). Projects built on it, such as Kata Containers and firecracker-containerd, fall back to block devices for the same reason ([firecracker-containerd docs](https://github.com/firecracker-microvm/firecracker-containerd/blob/main/docs/remote-snapshotter.md)). Live project directories would need block devices or a network filesystem. jailoc needs virtio-fs shares on both hosts.

## microsandbox

[microsandbox](https://github.com/superradcompany/microsandbox) is the closest match. It runs OCI images in libkrun micro-VMs on macOS (Apple silicon), Linux (KVM) and Windows, embeds as an SDK (Rust, TypeScript, Python, Go, Ruby) without a daemon, and checks guest traffic against a host-controlled policy that blocks private networks by default and supports host and port allowlists ([networking docs](https://docs.microsandbox.dev/networking/overview)).

The difference is ownership of the guest. microsandbox spawns its own `msb` runtime and guest agent (`agentd`) and exposes an exec-style API ([SDK setup](https://docs.microsandbox.dev/sdk/setup)). jailoc needs its own init inside the guest: jailoc-guest runs the SSH server for interactive agent sessions, receives secrets over SSH into tmpfs, applies staged snapshots, enforces `protect_git`, and runs a nested Docker daemon for `enable_docker`. Its network policy also follows jailoc's config model (`allowed_hosts` resolved through filtered DNS, `allowed_networks`, IPv4 and IPv6). go-microvm exposes the VM and leaves the guest to jailoc. microsandbox also describes itself as beta software with breaking changes expected.

## General-purpose VMs (Lima, Colima, OrbStack, Vagrant)

These start long-lived Linux VMs with a full distribution. They provide the kernel boundary but not the rest: no per-workspace VM by default, no egress policy enforced outside the guest, and mounts that are usually configured broadly (often the whole home directory). jailoc would end up as a layer of scripts around a VM it does not control.

## Apple Containerization

Apple's [`container`](https://github.com/apple/container) tool runs each container in its own lightweight VM with Virtualization.framework. It is macOS-only and relies on macOS 26 features, so it cannot cover Linux hosts. Containers attach to vmnet networks, which give each container an IP on a NAT or host-only subnet and isolate networks from each other ([networking docs](https://github.com/apple/container/blob/main/docs/networking.md), [technical overview](https://github.com/apple/container/blob/main/docs/technical-overview.md)). There is no per-destination egress policy, so blocking private networks while allowing selected internal hosts would need a separate enforcement point.

## What jailoc uses

libkrun provides the VM on both hosts (Hypervisor.framework on macOS, KVM on Linux), virtio-fs for shares, and a virtio-net interface whose only peer is jailoc's broker. go-microvm wraps it as a Go library, and jailoc downloads a pinned, checksum-verified runtime. See [Architecture](architecture.md) for the components and [Threat Model](threat-model.md) for what the boundary protects against.

## Design choices

Inside the VM boundary, jailoc makes a few choices of its own, each over an alternative that other sandboxes use.

### Network policy on the host, not in the guest

Firewall rules inside the guest (iptables, nftables) would be in reach of guest root, and an HTTP proxy configured through `HTTP_PROXY` only covers tools that honour it. jailoc's broker sits on the host as the VM's only network peer, so every packet passes it whatever the guest does. It decides on destination addresses only: it does not decrypt TLS or read host names from traffic, and DNS answers alone never open a private address. See [Network Isolation](network-isolation.md).

### Public allowed, private denied

The default policy allows the public internet and denies private and reserved ranges. An allow-only-listed-hosts default would stop more, but agents need package registries, model APIs and documentation from many hosts. The default protects internal infrastructure; it does not stop the agent from sending code to a public server. See [Threat Model](threat-model.md).

### Sessions over SSH

The agent does not run behind an HTTP server on a published port with a password, as in jailoc v1, or behind an exec API like microsandbox's. jailoc attaches to the guest's SSH server with a per-workspace key and a pinned host key, and a session holder inside the VM keeps the agent running when the connection drops, so a terminal can re-attach later. See [Architecture](architecture.md#sessions).

### Images from Dockerfiles

Workspaces use ordinary Dockerfiles rather than custom VM disk images or provisioning scripts. jailoc builds them with host Docker (or a builder VM with `isolated_build`) and unpacks the result into the VM's root file system. Users keep a format they know, and Docker never runs the workspace. See [Image Resolution](../reference/image-resolution.md).

### Secrets inside the VM

jailoc reads secrets on the host and delivers them over SSH into guest memory, so they never sit in the image, on a share or on the host disk. The agent can still read their values, because tools in the VM need them. microsandbox takes a different approach: it substitutes secrets on the host side of the network so the guest never sees the real value. See [Secrets](secrets.md).
