# Alternatives

Many tools can keep a coding agent away from the rest of a machine. They differ in what forms the boundary (namespaces, a policy in the host kernel, a userspace kernel, or a hardware VM), in who enforces network policy, and in whether they run locally. This page compares jailoc with them and explains the choices behind jailoc's design.

Facts about third-party tools describe their documented behaviour and default setup. Many of them can be combined or extended (a container behind a filtering proxy, a VM with custom firewall rules); the comparison covers what each tool provides on its own.

## What jailoc sets out to provide

jailoc is built around five requirements:

- **A separate kernel.** The agent runs as root with `sudo` and may run its own Docker daemon. A kernel bug or a misconfigured privilege must end in the guest, not on the host.
- **Network policy enforced outside the sandbox.** Root in the guest must not be able to change it. Private and reserved networks, cloud metadata and the host itself are denied by default; the public internet is allowed. See [Network Isolation](network-isolation.md).
- **Local and unmodified agents.** Workspaces run on the developer's machine, with the agent's own CLI and TUI, against the developer's checkout.
- **macOS and Linux hosts.** Apple silicon Macs and Linux with KVM, with the same behaviour.
- **Ordinary Docker images.** Workspace images are Dockerfiles, so existing toolchains and presets carry over.

No single alternative below meets all five out of the box. Some meet more of them than others, and some deliberately aim elsewhere.

## Overview

| Tool | Boundary | Hosts | Egress control | Built for agents |
|------|----------|-------|----------------|------------------|
| **jailoc** | Hardware VM per workspace (libkrun) | macOS (Apple silicon), Linux (KVM) | Userspace broker on the host: IP policy, private networks denied, filtered DNS | Yes |
| Docker / Podman | Container, shared kernel | Linux; macOS and Windows through one Linux VM | Network on or off, custom networks | No |
| Dev Containers | Container, shared kernel | Wherever Docker runs | None in the spec; optional iptables script | Partly |
| container-use | Container per agent branch | Wherever Docker runs | None | Yes |
| systemd-nspawn | Container, shared kernel | Linux | Network on, off, or a private veth | No |
| Toolbox, Distrobox | None (shares home, network, devices) | Linux | None | No |
| gVisor (`runsc`) | Userspace kernel | Linux | Host networking, no allowlist | No |
| Sysbox | Container with user namespace, shared kernel | Linux | Docker networking | No |
| Kata Containers | Hardware VM per pod | Linux (KVM) | CNI / Kubernetes network policy | No |
| bubblewrap, firejail, nsjail | Namespaces, seccomp | Linux | Network on or off | No |
| Landlock | Kernel access-control rules | Linux | TCP port rules | No |
| Seatbelt (`sandbox-exec`) | Kernel policy (MAC) | macOS | IP and port rules | No |
| Claude Code, Codex, Gemini CLI sandboxes | Seatbelt, bubblewrap, or a container | macOS, Linux (Windows for some) | Proxy with a domain allowlist (Claude Code, Codex) | Yes |
| Microsoft MXC | Per-OS backend (Seatbelt, bubblewrap, Windows containers, experimental micro-VM) | Windows, Linux, macOS | Policy-defined, backend-dependent | Yes |
| Firecracker, Cloud Hypervisor, QEMU `microvm` | Hardware VM | Linux (Cloud Hypervisor also Windows) | None; TAP and NAT are left to the user | No |
| Lima, Colima, OrbStack, Vagrant | General-purpose Linux VM | macOS, Linux | None beyond the VM's NAT | No |
| Incus, LXD | System container or full VM | Linux | Network ACLs by address, port and protocol | No |
| Apple `container` | Hardware VM per container | macOS (Apple silicon) | None | No |
| Docker Sandboxes (`sbx`) | Hardware VM per sandbox | macOS, Windows, Linux | HTTP(S) proxy with allow and deny lists | Yes |
| microsandbox | Hardware VM per sandbox (libkrun) | macOS (Apple silicon), Linux (KVM) | Private networks denied, allowlist, DNS filtering | Yes |
| E2B, Modal, Daytona, Codespaces | Hosted VM or container | Remote | IP, CIDR or domain allowlists (varies) | Mostly |

## Containers

### Docker and Podman

A container is a set of namespaces and cgroups on the host kernel. Every syscall the agent makes reaches the host kernel, so a kernel vulnerability, a mounted Docker socket, or a privileged flag is enough to leave it. Giving the agent root and a Docker daemon of its own inside a container needs `--privileged` or Docker-in-Docker, which removes most of what the container isolated.

On macOS and Windows, Docker Desktop and Podman run all containers inside one Linux VM. That VM separates containers from the host OS, but all containers and the daemon share its kernel, and the VM mounts large parts of the host's home directory by default.

Networking is all or nothing: a container on the default bridge reaches the host's LAN, VPN and cloud metadata endpoint, and `--network none` cuts everything. Filtering by destination needs an external proxy or iptables rules.

jailoc uses Docker only to build images, never to run workspaces. Its v1 ran agents in Docker containers; `jailoc migrate` converts such configurations.

### Dev Containers

The [Dev Containers](https://containers.dev) specification describes a development environment in `devcontainer.json`, implemented by VS Code, GitHub Codespaces and other tools. It inherits the container boundary. The specification has no network policy; Anthropic's reference container for Claude Code adds an `init-firewall.sh` script that sets iptables to drop by default and allows a list of IP ranges resolved at startup. The rules run inside the container with `NET_ADMIN`, so anything that gains root in the container can remove them, and a domain that changes addresses after startup is not covered.

### container-use

[container-use](https://github.com/dagger/container-use) gives each agent its own container and Git branch through an MCP server, with commands to watch, enter and merge its work. It addresses parallel agents stepping on each other more than containment, and has no network policy of its own.

### systemd-nspawn

`systemd-nspawn` starts a container from a directory or disk image with systemd's namespace setup, without a separate daemon. It shares the host kernel like any container, and its network options are the host's network, none, or a private virtual Ethernet link that the host then has to route and filter.

### Toolbox and Distrobox

[Toolbox](https://containertoolbx.org) and [Distrobox](https://distrobox.it) run another Linux distribution's userland in a container that is integrated with the host on purpose: the home directory, network, devices, display and session bus are shared. They are a way to install tools, not a sandbox, and an agent in one has the same reach as on the host.

## Stronger container runtimes

### gVisor

[gVisor](https://gvisor.dev) runs a kernel written in Go in userspace (the Sentry), which handles the container's syscalls instead of the host kernel. It is an OCI runtime (`runsc`) for Docker and Kubernetes. The host kernel remains shared, but the Sentry only reaches it through a narrow, seccomp-filtered set of syscalls, which shrinks the attack surface considerably. gVisor runs only on Linux. Its network stack, netstack, runs in the Sentry, but egress policy is left to the host network; it has no destination allowlist.

jailoc's network broker uses the same gVisor network stack as a library, on the host side of the VM. The guest has a real Linux kernel; gVisor terminates its packets and applies policy where the guest cannot reach it. Running the whole workspace under `runsc` would require Linux hosts and a Docker daemon, and would leave Docker-in-Docker and other kernel features with gVisor's compatibility limits.

### Sysbox

[Sysbox](https://github.com/nestybox/sysbox) is an OCI runtime (`sysbox-runc`) that lets containers run systemd, Docker and Kubernetes inside them without `--privileged`. It maps root in the container to an unprivileged user on the host and virtualises parts of `/proc` and `/sys`. It makes Docker-in-Docker much safer than a privileged container, but the boundary is still the shared host kernel, it runs only on Linux, and networking is ordinary Docker networking.

### Kata Containers

[Kata Containers](https://katacontainers.io) is an OCI runtime that starts each pod or container in a lightweight VM, using QEMU, Cloud Hypervisor, Firecracker or its built-in Dragonball VMM. It provides a hardware VM boundary with container tooling. It targets Kubernetes and containerd on Linux hosts with KVM, has no macOS host support, and leaves networking to CNI.

## Process sandboxes

Process sandboxes restrict the agent's process tree on the host kernel. They start fast and need no image, but the agent sees the host's filesystem layout, tools and kernel, and cannot get root or a Docker daemon of its own.

### bubblewrap, firejail and nsjail

[bubblewrap](https://github.com/containers/bubblewrap) builds unprivileged user, mount, PID and network namespaces; it underlies Flatpak and the Linux sandboxes of Claude Code and Codex. [firejail](https://github.com/netblue30/firejail) adds seccomp, capabilities and AppArmor with hundreds of application profiles, and is installed setuid root. [nsjail](https://github.com/google/nsjail) is Google's configurable jail with a seccomp policy language and cgroups. All three run only on Linux and offer the network either fully or not at all, so filtering by destination needs a proxy outside the sandbox.

### Landlock

[Landlock](https://docs.kernel.org/userspace-api/landlock.html) is a Linux security module that lets an unprivileged process restrict its own filesystem access and, from ABI 4, the TCP ports it binds and connects to. It filters by port, not by address or domain. jailoc applies Landlock to the VMM process on Linux, so the virtio-fs server can reach only the shared paths (see [Security Boundaries](security-boundaries.md)).

### Seatbelt

Seatbelt is the macOS kernel sandbox behind `sandbox-exec`, configured with profiles in Apple's sandbox profile language. Apple marks `sandbox-exec` deprecated and publishes no documentation for the language, but it ships with macOS and is widely used. Profiles can limit file access and network traffic by address and port; agent sandboxes typically allow only a local proxy port and filter domains in the proxy.

### Built-in agent sandboxes

Several agents ship their own sandbox for the commands they run:

- **Claude Code** runs shell commands under Seatbelt on macOS and bubblewrap on Linux, through the `@anthropic-ai/sandbox-runtime` library. Writes are limited to the working directory, and network traffic goes through a host proxy with a domain allowlist. Claude Code's own file tools, MCP servers and hooks run outside the sandbox.
- **Codex CLI** uses Seatbelt on macOS and bubblewrap with seccomp on Linux. In its default mode commands can write to the workspace and have no network; a proxy can allow listed domains.
- **Gemini CLI** can run commands under Seatbelt profiles, in a Docker or Podman container, or under gVisor on Linux.

These sandboxes are integrated with the agent and cost almost nothing to start. They share the host kernel, cover the agent's tool calls rather than the whole agent process, and differ per agent. A domain-allowlist proxy constrains HTTP and TLS traffic well, but a policy expressed as domains says nothing about which addresses those domains resolve to, so private networks are reachable through any allowed name that points to them. jailoc confines the whole agent process and every tool, MCP server and container it starts, with the same policy for each agent it supports. The two approaches combine: an agent's own sandbox can run inside a jailoc workspace.

### Microsoft MXC

[Microsoft eXecution Container (MXC)](https://github.com/microsoft/mxc) is a common JSON policy format over per-OS backends: process containers, Windows Sandbox and WSL on Windows, bubblewrap or LXC on Linux, Seatbelt on macOS, and an experimental micro-VM backend. Isolation strength depends on the backend; on macOS it is Seatbelt. Its own documentation states that its profiles are not to be treated as security boundaries yet.

## Virtual machine monitors

### Firecracker, Cloud Hypervisor and QEMU microvm

[Firecracker](https://firecracker-microvm.github.io) and [Cloud Hypervisor](https://www.cloudhypervisor.org) are minimal Rust VMMs on KVM, built for serverless functions and cloud workloads; QEMU's `microvm` machine type trims QEMU in the same direction. They provide the hardware boundary jailoc needs, but they are building blocks: they run on Linux only (Cloud Hypervisor also on Windows with MSHV), and image handling, file sharing, guest init, sessions and network policy are left to the caller. Firecracker in particular has no shared filesystem device, so a checkout has to be copied in as a block device rather than shared.

### libkrun

[libkrun](https://github.com/containers/libkrun) is a VMM library with its own guest kernel build (libkrunfw). It runs on KVM and on Hypervisor.framework on macOS, and has virtio-fs for sharing host directories. jailoc runs it through the go-microvm runner. libkrun's network backends hand guest packets to a host process, which is where jailoc's broker sits. The same library powers krunvm, krunkit (a Lima driver) and Podman's krun runtime.

Running on both macOS and Linux with one VMM, with directory sharing and a host-side network hook, is the reason jailoc uses libkrun rather than Firecracker or Cloud Hypervisor (Linux only) or Apple's Virtualization.framework (macOS only).

## Virtual machine managers

### Lima, Colima, OrbStack and Vagrant

[Lima](https://lima-vm.io) runs Linux VMs on macOS and Linux with Virtualization.framework, QEMU or krunkit, and [Colima](https://github.com/abiosoft/colima) builds a Docker host on top of it. [OrbStack](https://orbstack.dev) runs Docker and Linux machines on macOS in one shared VM. [Vagrant](https://www.vagrantup.com) manages VMs from a Vagrantfile on many hypervisors. They are designed for convenience: by default they mount the user's home directory, forward ports to the host and give the VM ordinary NAT access to every network the host can reach. They can be locked down, but a policy that denies private networks has to be built separately, and one VM is typically shared by many projects.

### Incus and LXD

[Incus](https://linuxcontainers.org/incus/) and Canonical's [LXD](https://canonical.com/lxd), from which Incus was forked, manage both system containers and full QEMU VMs from the same images and CLI. They have network ACLs that allow or deny traffic by address, port and protocol, so a VM with a private-network deny list can be assembled from their parts. They run on Linux hosts, need a daemon with root privileges, and leave image builds, shares, sessions and the policy itself to be configured per instance.

### Apple container

Apple's [`container`](https://github.com/apple/container) tool runs each Linux container in its own lightweight VM on Virtualization.framework, with OCI images. It provides a per-container kernel on Apple silicon Macs, but has no Linux host support and no egress policy beyond the VM's network.

## Agent sandboxes built on VMs

### Docker Sandboxes

[Docker Sandboxes](https://docs.docker.com/ai/sandboxes/) run each agent in a micro-VM with its own kernel and its own Docker daemon, using a VMM Docker built for the purpose, through the `sbx` CLI. The workspace is synchronised into the sandbox at the same path. A host-side HTTP and HTTPS proxy applies allow and deny lists per sandbox, and credentials can be injected at that proxy so the sandbox never holds them. It supports many agents and is the closest product to jailoc in shape.

The main differences are in the network model and the shares. Docker Sandboxes filter at the HTTP proxy by domain; jailoc filters every TCP and UDP flow by destination address, denies private and reserved networks regardless of names, filters DNS, and logs each connection with its SNI or HTTP host. jailoc shares host directories over virtio-fs with read-only shares enforced by the VMM, protects Git hooks and config, and builds workspace images from plain Dockerfiles.

### microsandbox

[microsandbox](https://github.com/superradcompany/microsandbox) runs OCI images in libkrun micro-VMs, like jailoc, and targets agents and untrusted code through SDKs and an MCP server. Its default network policy is close to jailoc's: public internet allowed, private, host-local, link-local and metadata destinations blocked, with DNS filtering, optional allowlists, and secret substitution at the network layer. It is a programmable sandbox for code an application sends to it, with snapshots and forks; jailoc is a workspace manager for interactive agent sessions against a developer's checkout, with persistent agent state, SSH sessions and per-workspace configuration.

### Arrakis

[Arrakis](https://github.com/abshkbh/arrakis) is a self-hosted micro-VM sandbox on Cloud Hypervisor with a REST API, a Python SDK, an MCP server and snapshots. It runs on Linux and documents no egress policy.

## Hosted sandboxes

[E2B](https://e2b.dev) runs Firecracker micro-VMs, [Modal](https://modal.com) runs sandboxes under gVisor or in VMs, and [Daytona](https://www.daytona.io) offers container and VM sandboxes; each has network allowlists by IP range or domain. GitHub Codespaces runs Dev Containers in a cloud VM per codespace. Cloud sandboxes keep untrusted code entirely off the developer's machine, which is the strongest separation from the host. In exchange the code, and any credentials the agent needs, are sent to a third party, the sandbox reaches the internet from the provider's network rather than the developer's, and local tools, VPN-only services and the developer's checkout are not directly available.

## Why a userspace broker instead of firewall rules

Most local sandboxes that filter traffic either run a firewall inside the sandbox or an HTTP proxy outside it. jailoc does neither:

- **Firewall rules inside the sandbox** are under the control of anything that gets root there. jailoc's guest has root by design, so its policy has to live on the host.
- **Host firewall rules** (iptables, nftables, pf) need root on the host, differ between Linux and macOS, and are shared state that VPN clients, Docker and other tools also modify.
- **An HTTP proxy** sees only traffic that is sent to it and decides by domain name. Raw TCP, UDP, SSH and DNS need separate handling, and a domain says nothing about the address it resolves to.

The guest's only network device is connected to a per-workspace broker process, which runs a full TCP/IP stack in userspace and opens a host socket only for flows its policy allows. It needs no privileges, behaves the same on macOS and Linux, judges every flow by its real destination address, and stops the VM when it exits, so there is no path around it. See [Network Isolation](network-isolation.md) for the policy itself.

## When another tool fits better

- **Linux servers or Kubernetes** with many untrusted workloads: Kata Containers or gVisor.
- **A sandbox an application calls programmatically** for model-generated code: microsandbox, E2B, Modal or Daytona.
- **Lowest overhead**, accepting a shared kernel: the agent's own sandbox, or bubblewrap or Seatbelt directly.
- **Windows hosts**: Docker Sandboxes, MXC or WSL-based setups; jailoc does not run on Windows.
- **Keeping all agent activity off the developer's machine**: a hosted sandbox.

Whatever the boundary, the agent can still read what it is given and write to what is shared with it, and an allowed destination can still receive data. See [Threat Model](threat-model.md) for the risks that remain with jailoc.
