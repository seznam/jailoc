# How jailoc Works

jailoc is a CLI tool that runs [OpenCode](https://opencode.ai) coding agents inside per-workspace micro-VMs. Each workspace boots its own libkrun virtual machine with its own kernel, sees only the host directories shared with it, and reaches the network only through a host-side broker that blocks private infrastructure while leaving the public internet open.

The problem it solves is simple to state. An autonomous coding agent running directly on a machine can read any file the user can read, call any network endpoint, and change anything the user can touch. jailoc draws a hardware-virtualization boundary around it. Inside the VM the agent can do what it likes, including becoming root. Outside it, the agent has only what the workspace configuration grants.

## The startup sequence

`jailoc up` runs these steps in order:

**1. Config loading.** jailoc reads `~/.config/jailoc/config.toml`, writing a default one on first run. jailoc v1 settings (`backend = "docker"`, `mode`, `password_mode`, `expose_port`) are rejected with a pointer to [`jailoc migrate`](../how-to/migrate-from-v1.md).

**2. Workspace resolution.** The workspace name becomes a resolved description: shared paths and mounts, snapshots, allowed hosts and networks, secrets, CPU and memory.

**3. Runtime check.** On first use jailoc downloads the pinned VM runtime (the go-microvm runner, libkrun and the libkrunfw guest kernel) and verifies its SHA-256. Supported hosts are Apple Silicon Macs (Hypervisor.framework) and Linux on amd64 (KVM).

**4. Image build and export.** The workspace image is built with Docker, either on the host daemon or inside a builder VM, then exported and unpacked into a local image cache. See [Image Resolution](../reference/image-resolution.md).

**5. VM start.** A detached supervisor starts the network broker and boots the VM from a copy-on-write clone of the unpacked image. `jailoc-guest`, the VM's init process, mounts the shares, prepares the `agent` user and starts an SSH server. jailoc then pushes secrets and environment, read on the host before the boot, over SSH.

Sessions run over SSH: `jailoc` attaches to a running OpenCode session or starts a new one. No OpenCode server port is published on the host and no server password exists. See [Manage sessions](../how-to/sessions.md).

`jailoc reload` applies network policy, published ports, the SSH agent, the CA bundle, snapshots, environment and secrets to a running VM. Changes to shares, CPU, memory, `enable_docker` or `protect_git` take effect after `jailoc restart`.

## Why a VM?

A container shares the host kernel, so a kernel exploit or a misconfigured privilege ends on the host. A VM has its own kernel, and the host exposes only a narrow set of devices to it: virtio-fs shares, one virtio-net interface whose only peer is the broker, and a few Unix sockets the broker forwards. That boundary lets jailoc give the agent root and `sudo` inside the guest without giving it anything on the host. It also lets the agent run its own Docker daemon (`enable_docker`) without touching the host's.

Docker on the host is used only to build images (unless the builder VM does it) and by `jailoc migrate`. Workspaces never run on it.

See [Architecture](architecture.md) for the components and [Threat Model](threat-model.md) for what the boundary protects against.
