# Why Not Something Else?

jailoc runs each workspace in its own libkrun micro-VM, driven through go-microvm, with a host-side network broker. Several other isolation tools exist. This page explains why jailoc does not build on them.

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

Firecracker is a minimal KVM-based VMM. It is Linux-only and deliberately has no shared-filesystem device, so live project directories would need block devices or a network filesystem. jailoc needs virtio-fs shares on both hosts.

## microsandbox

microsandbox also runs workloads in libkrun micro-VMs, so the isolation model is close to jailoc's. It is designed as a sandbox server with SDKs for running untrusted code snippets, and it owns the VM lifecycle, image handling and networking. jailoc needs control over those pieces: jailoc-guest as init, SSH-based agent sessions, secrets delivered over SSH into tmpfs, staged snapshots, and a broker that enforces `allowed_hosts` and `allowed_networks` per workspace. Building on go-microvm, a Go library over libkrun, keeps those in the jailoc binary.

## General-purpose VMs (Lima, Colima, OrbStack, Vagrant)

These start long-lived Linux VMs with a full distribution. They provide the kernel boundary but not the rest: no per-workspace VM by default, no egress policy enforced outside the guest, and mounts that are usually configured broadly (often the whole home directory). jailoc would end up as a layer of scripts around a VM it does not control.

## Apple Containerization

Apple's `container` tool runs each container in its own lightweight VM with Virtualization.framework. It is macOS-only, so it cannot cover Linux hosts, and its networking does not give jailoc a host-side point to enforce per-workspace egress policy.

## What jailoc uses

libkrun provides the VM on both hosts (Hypervisor.framework on macOS, KVM on Linux), virtio-fs for shares, and a virtio-net interface whose only peer is jailoc's broker. go-microvm wraps it as a Go library, and jailoc downloads a pinned, checksum-verified runtime. See [Architecture](architecture.md) for the components and [Threat Model](threat-model.md) for what the boundary protects against.
