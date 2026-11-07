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

✅ meets jailoc's requirement · ⚠️ partly, or only with extra setup · ❌ does not. In the License column: ✅ open source and free · ⚠️ mixed, or paid parts · ❌ proprietary

| Tool | Boundary | Hosts | Egress control | Built for agents | License |
|------|----------|-------|----------------|------------------|---------|
| **jailoc** | ✅ Hardware VM per workspace (libkrun) | macOS (Apple silicon), Linux (KVM) | ✅ Userspace broker on the host: IP policy, private networks denied, filtered DNS | ✅ | ✅ MIT |
| Docker / Podman | ❌ Container, shared kernel | Linux; macOS and Windows through one Linux VM | ❌ Network on or off, custom networks | ❌ | ⚠️ Engine and Podman Apache-2.0; Docker Desktop paid in larger companies |
| Dev Containers | ❌ Container, shared kernel | Wherever Docker runs | ⚠️ None in the spec; optional iptables script | ⚠️ Partly | ⚠️ Spec and CLI MIT; VS Code extension proprietary |
| container-use | ❌ Container per agent branch | Wherever Docker runs | ❌ None | ✅ | ✅ Apache-2.0 |
| agent-sandbox | ❌ Container per workspace, shared kernel | Linux (rootless Docker; Podman untested) | ⚠️ Hostname allowlist for HTTP(S) in a proxy container; CONNECT limited to port 443 by default; no resolved-IP or DNS filtering | ✅ | ❌ No license declared |
| systemd-nspawn | ❌ Container, shared kernel | Linux | ❌ Network on, off, or a private veth | ❌ | ✅ LGPL-2.1 |
| Toolbox, Distrobox | ❌ None (shares home, network, devices) | Linux | ❌ None | ❌ | ✅ Apache-2.0, GPL-3.0 |
| gVisor (`runsc`) | ⚠️ Userspace kernel | Linux | ❌ Host networking, no allowlist | ❌ | ✅ Apache-2.0 |
| Sysbox | ❌ Container with user namespace, shared kernel | Linux | ❌ Docker networking | ❌ | ✅ Apache-2.0 |
| Kata Containers | ✅ Hardware VM per pod | Linux (KVM) | ⚠️ CNI / Kubernetes network policy | ❌ | ✅ Apache-2.0 |
| bubblewrap, firejail, nsjail | ❌ Namespaces, seccomp | Linux | ❌ Network on or off | ❌ | ✅ LGPL-2.0, GPL-2.0, Apache-2.0 |
| Landlock | ❌ Kernel access-control rules | Linux | ⚠️ TCP port rules | ❌ | ✅ GPL-2.0 (Linux kernel) |
| Seatbelt (`sandbox-exec`) | ❌ Kernel policy (MAC) | macOS | ⚠️ IP and port rules | ❌ | ❌ Closed, part of macOS |
| Claude Code, Codex, Gemini CLI sandboxes | ❌ Seatbelt, bubblewrap, or a container | macOS, Linux (Windows for some) | ⚠️ Proxy with a domain allowlist (Claude Code, Codex) | ✅ | ⚠️ Codex, Gemini CLI and Claude Code's sandbox runtime Apache-2.0; Claude Code proprietary |
| Microsoft MXC | ⚠️ Per-OS backend (Seatbelt, bubblewrap, Windows containers, experimental micro-VM) | Windows, Linux, macOS | ⚠️ Policy-defined, backend-dependent | ✅ | ✅ MIT |
| Firecracker, Cloud Hypervisor, QEMU `microvm` | ✅ Hardware VM | Linux (Cloud Hypervisor also Windows) | ❌ None; TAP and NAT are left to the user | ❌ | ✅ Apache-2.0, Apache-2.0/BSD-3, GPL-2.0 |
| Lima, Colima, OrbStack, Vagrant | ⚠️ General-purpose Linux VM, shared by projects | macOS, Linux | ❌ None beyond the VM's NAT | ❌ | ⚠️ Lima Apache-2.0, Colima MIT; OrbStack proprietary, paid for commercial use; Vagrant BUSL-1.1 |
| Incus, LXD | ⚠️ System container or full VM | Linux | ⚠️ Network ACLs by address, port and protocol | ❌ | ✅ Apache-2.0, AGPL-3.0 |
| Apple `container` | ✅ Hardware VM per container | macOS (Apple silicon) | ❌ None | ❌ | ✅ Apache-2.0 |
| Docker Sandboxes (`sbx`) | ✅ Hardware VM per sandbox | macOS, Windows, Linux | ✅ HTTP(S) proxy with allow and deny lists | ✅ | ❌ Proprietary; free locally, including commercial use; cloud and organization policies paid |
| microsandbox | ✅ Hardware VM per sandbox (libkrun) | macOS (Apple silicon), Linux (KVM) | ✅ Private networks denied, allowlist, DNS filtering | ✅ | ✅ Apache-2.0 |
| E2B, Modal, Daytona, Codespaces | ✅ Hosted VM or container, off the developer's machine | Remote | ⚠️ IP, CIDR or domain allowlists (varies) | ⚠️ Mostly | ⚠️ Paid services; E2B (Apache-2.0) and Daytona (AGPL-3.0) can be self-hosted |

## Containers

### Docker and Podman

Containers share the host kernel, so a kernel bug, a mounted Docker socket or `--privileged` is enough to escape; giving the agent its own Docker daemon needs exactly that. Networking is all or nothing, with the host's LAN, VPN and cloud metadata reachable by default. On macOS, all containers share one Linux VM that mounts much of the home directory. jailoc uses Docker only to build images; its v1 ran agents in containers.

### Dev Containers

[Dev Containers](https://containers.dev) describe an environment in `devcontainer.json` and inherit the container boundary. Anthropic's Claude Code reference adds an iptables allowlist inside the container, which root in the container can remove and which resolves domains only once at startup.

### container-use

[container-use](https://github.com/dagger/container-use) gives each agent a container and Git branch through MCP. It keeps parallel agents apart, not contained, and has no network policy.

### agent-sandbox

[agent-sandbox](https://github.com/jurca/agent-sandbox) runs Claude Code or Copilot CLI in a container per workspace on Linux, with rootless Docker as the tested engine. Its threat model is accidents and prompt injection, and it leaves kernel and runtime escapes to a VM. The workspace is mounted at its host path. `.git` is read-only, and so are agent and editor configuration directories such as `.claude`, `.vscode` and `.devcontainer`. Dependency directories such as `.venv` and `node_modules` get per-workspace volumes, so the agent cannot plant code in the dependency trees that host tools run. The rest of the workspace stays writable, so build scripts, tests and CI workflows can still carry code that later runs outside the container. Every change to the project's sandbox configuration needs approval on the host before it takes effect.

Egress has two tiers. The container's only route out is a tinyproxy container that forwards HTTP and HTTPS to listed hostnames, model APIs by default, and limits CONNECT tunnels to port 443 by default. A timed `net` session adds more hosts or opens everything except localhost, private IP literals and the host's `NO_PROXY` entries, unless one of them is listed, and shows the hosts it blocked. Allowed names are not checked against the addresses they resolve to, so a listed name that resolves to a private address stays reachable. Before anything starts, it refuses `.env` files in the workspace unless they are symlinks into a host directory outside it and ignored by a tracked `.gitignore`. Docker inside the sandbox is not supported, and the agent does not get root. jailoc boots a separate kernel, enforces IP and DNS policy and allows the public internet by default.

### systemd-nspawn

`systemd-nspawn` runs a container from a directory or image without a daemon. It shares the host kernel, and a private network link still has to be routed and filtered by the host.

### Toolbox and Distrobox

[Toolbox](https://containertoolbx.org) and [Distrobox](https://distrobox.it) share the home directory, network and devices with the host by design. They are for installing tools, not sandboxing.

## Stronger container runtimes

### gVisor

[gVisor](https://gvisor.dev) (`runsc`) handles container syscalls in a userspace kernel, leaving only a small, seccomp-filtered surface of the host kernel exposed. It runs only on Linux and has no egress allowlist. jailoc's broker uses gVisor's network stack on the host side of the VM, where the guest cannot reach it.

### Sysbox

[Sysbox](https://github.com/nestybox/sysbox) runs Docker and systemd inside containers without `--privileged` by mapping container root to an unprivileged host user. The host kernel is still shared, it runs only on Linux, and networking is plain Docker.

### Kata Containers

[Kata Containers](https://katacontainers.io) starts each pod in a lightweight VM behind a standard OCI runtime. It targets Kubernetes on Linux with KVM and leaves networking to CNI.

## Process sandboxes

Process sandboxes restrict the agent's processes on the host kernel. They start instantly and need no image, but the agent sees the host's files and tools and cannot get root or its own Docker daemon.

### bubblewrap, firejail and nsjail

[bubblewrap](https://github.com/containers/bubblewrap), [firejail](https://github.com/netblue30/firejail) (setuid root) and [nsjail](https://github.com/google/nsjail) build namespaces and seccomp filters on Linux. Network is either on or off; filtering by destination needs an outside proxy.

### Landlock

[Landlock](https://docs.kernel.org/userspace-api/landlock.html) lets an unprivileged Linux process restrict its own file access and TCP ports, not addresses or domains. jailoc applies it to the VMM so virtio-fs reaches only shared paths (see [Security Boundaries](security-boundaries.md)).

### Seatbelt

Seatbelt (`sandbox-exec`) is the macOS kernel sandbox, deprecated and undocumented but widely used. Profiles limit files and IP/port traffic; agent sandboxes usually allow only a local filtering proxy.

### Built-in agent sandboxes

Claude Code and Codex CLI run commands under Seatbelt on macOS and bubblewrap on Linux, with a host proxy that allows listed domains; Gemini CLI uses Seatbelt, a container or gVisor. They are cheap and integrated, but share the host kernel, cover tool calls rather than the whole agent (Claude Code's file tools, MCP servers and hooks run outside), and differ per agent. A domain allowlist also says nothing about what a domain resolves to, so private addresses behind an allowed name stay reachable. They can run inside a jailoc workspace.

### Microsoft MXC

[MXC](https://github.com/microsoft/mxc) is one JSON policy format over per-OS backends: Windows containers and Sandbox, bubblewrap or LXC on Linux, Seatbelt on macOS, and an experimental micro-VM. Its documentation says its profiles are not yet security boundaries.

## Virtual machine monitors

### Firecracker, Cloud Hypervisor and QEMU microvm

[Firecracker](https://firecracker-microvm.github.io), [Cloud Hypervisor](https://www.cloudhypervisor.org) and QEMU's `microvm` provide the VM boundary but run only on Linux (Cloud Hypervisor also on Windows) and leave images, shares, sessions and network policy to the caller. Firecracker has no shared filesystem at all.

### libkrun

[libkrun](https://github.com/containers/libkrun) is a VMM library that runs on both KVM and macOS Hypervisor.framework, shares directories over virtio-fs, and hands guest packets to a host process. That combination is why jailoc uses it, through the go-microvm runner.

## Virtual machine managers

### Lima, Colima, OrbStack and Vagrant

[Lima](https://lima-vm.io), [Colima](https://github.com/abiosoft/colima), [OrbStack](https://orbstack.dev) and [Vagrant](https://www.vagrantup.com) run general-purpose VMs built for convenience: home directory mounted, ports forwarded, NAT to every network the host reaches, often one VM for all projects. A private-network deny policy has to be built separately.

### Incus and LXD

[Incus](https://linuxcontainers.org/incus/) and [LXD](https://canonical.com/lxd) manage system containers and VMs on Linux, with network ACLs by address, port and protocol. They need a root daemon, and shares, sessions and policy are configured per instance.

### Apple container

Apple's [`container`](https://github.com/apple/container) runs each container in its own VM on Apple silicon Macs. It has no Linux host support and no egress policy.

## Agent sandboxes built on VMs

### Docker Sandboxes

[Docker Sandboxes](https://docs.docker.com/ai/sandboxes/) (`sbx`) run each agent in a micro-VM with its own Docker daemon, sync the workspace in, and filter traffic through a host HTTP(S) proxy that can inject credentials. It is the closest product to jailoc. jailoc instead filters every TCP and UDP flow by destination address, denies private networks regardless of names, filters DNS, enforces read-only virtio-fs shares, protects Git hooks and builds images from plain Dockerfiles.

### microsandbox

[microsandbox](https://github.com/superradcompany/microsandbox) also runs OCI images in libkrun VMs, with a similar default policy (public internet allowed, private and metadata destinations blocked, DNS filtering). It is an SDK sandbox for code an application sends it; jailoc manages interactive agent workspaces with persistent state and SSH sessions.

### Arrakis

[Arrakis](https://github.com/abshkbh/arrakis) is a self-hosted Linux micro-VM sandbox on Cloud Hypervisor with an API and MCP server, and no documented egress policy.

## Hosted sandboxes

[E2B](https://e2b.dev), [Modal](https://modal.com), [Daytona](https://www.daytona.io) and GitHub Codespaces run sandboxes in the cloud, most with IP or domain allowlists. Nothing runs on the developer's machine, but code and credentials go to a third party, and local tools, VPN-only services and the local checkout are out of reach.

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
