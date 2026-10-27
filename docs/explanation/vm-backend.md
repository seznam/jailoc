# VM Backend

The default backend runs each workspace as containers on the host Docker daemon. Their isolation is the Linux kernel's namespace and cgroup isolation, plus an iptables firewall inside the agent container. `backend = "vm"` moves the agent into its own virtual machine, so a kernel exploit, a privilege escalation or a malicious build step ends inside a disposable VM instead of on the host or its Docker daemon.

## Architecture

```mermaid
flowchart LR
  subgraph host["Host"]
    jailoc["jailoc CLI"]
    sup["jailoc vm-supervise"]
    broker["jailoc netbroker<br/>userspace TCP/IP"]
    runner["go-microvm-runner<br/>(libkrun)"]
    cache["image cache"]
  end
  subgraph builder["Builder VM"]
    dockerd["dockerd"]
  end
  subgraph vm["Workspace VM"]
    guest["jailoc-guest<br/>init + SSH server"]
    agent["opencode (UID 1000)"]
  end
  jailoc -- "1. build: Docker API" --> dockerd
  dockerd -- "2. export image" --> cache
  cache -- "3. root file system" --> vm
  jailoc -- "4. SSH over Unix socket" --> broker
  sup --> broker
  sup --> runner
  runner -- "virtio-net frames" --> broker
  broker -- "policy-checked egress" --> internet["Internet"]
  guest --> agent
```

`jailoc up` runs in two phases:

1. **Build.** jailoc runs its regular image resolution, so workspace images, Dockerfile overlays and their build cache behave as with the Docker backend. It uses the host Docker daemon when one is available (`[vm] builder`), otherwise a builder VM: a pinned `docker:dind` image with a persistent data disk, reached through a Unix socket that the network broker forwards into the VM. The finished image is exported and unpacked into a local image cache; a builder VM then shuts down.
2. **Run.** The workspace VM boots the unpacked image as its root file system, with `jailoc-guest` as its init process. jailoc then delivers secrets over SSH.

Every VM is owned by a detached supervisor process that starts its network broker, boots the VM, waits until the VM is ready, and from then on keeps the two together: when either the VM or the broker exits, it stops the other.

The VM runtime (the runner process, libkrun and the libkrunfw guest kernel) is a separate download: jailoc pins its URL and SHA-256 per platform and fetches it on first use, so the jailoc binary stays free of C dependencies for users of the Docker backend. `jailoc-guest` is pure Go and is embedded in jailoc itself, so it always matches the jailoc version.

## Builder

Building with the host Docker daemon is fastest and shares the build cache with the Docker backend, but Dockerfile `RUN` steps then run with Docker-backend isolation and unrestricted host networking. A builder VM keeps build steps inside a VM boundary instead.

All VM workspaces share one builder VM. It keeps one build cache for all of them and reaches public addresses only. With `isolated_build`, a workspace gets a builder of its own, with a separate data disk and the workspace's `allowed_hosts` and `allowed_networks`, so build steps of one workspace cannot read another workspace's build cache or private network access. Builds of the same builder are serialized.

## Workspace VM

The root file system is a copy-on-write clone of the unpacked image and is discarded on every start; only `~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache` of the agent persist, in the workspace's VM state directory. `jailoc-guest` prepares the system and then serves SSH:

1. configures the network address, the broker as gateway and DNS resolver, and the hostname,
2. creates the `agent` user (UID 1000) when the image has none and its home directory,
3. mounts the shared directories, binds the copies (including the Git configuration) and adds a forwarded CA bundle to the system trust store,
4. adds the hostname to `/etc/hosts`; it leaves privileges alone, so the agent can use `sudo` from the image, because root in the VM is still inside the VM boundary,
5. starts an SSH server that accepts only the workspace's key and runs every session as `agent`.

`jailoc` opens a session by connecting to the SSH server through a Unix socket that the broker forwards into the VM. It authenticates with a per-workspace key and pins the guest's host key, which is generated anew for every start. The agent runs in a session held by `jailoc-guest`: a small process inside the VM owns the agent's terminal, so the agent keeps running when the SSH connection ends. jailoc relays keystrokes, output and window size changes to it. When a terminal re-attaches, the holder replays the terminal modes the agent had switched on (alternate screen, mouse reporting, bracketed paste, keyboard protocols) and makes the agent redraw by briefly changing the window size. Sessions live in VM memory and end when the VM stops. `jailoc down` asks the guest to stop over a control share, so open files are flushed before the VM powers off.

Secrets never touch the root file system or a share: jailoc sends them over SSH after the VM starts, and `jailoc-guest` writes them to `/run/secrets` (in memory, readable only by `agent`).

## Filesystem sharing

Workspace `paths`, the host directories of `mounts`, the workspace configuration directory, the workspace cache directory and the persistent home directories are shared into the VM with virtio-fs. Read-only shares are enforced by the VMM: guest root cannot remount them writable. Sharing works on directories only, which is why file mounts and the SSH agent socket are not available.

Files and directories listed in `copies`, and the Git configuration, are not shared: jailoc snapshots them into the VM state directory when the VM starts, shares that directory read-only, and the guest binds each item read-only at its destination. The workspace sees the content from the moment of the start, and its writes never reach the host original.

Shares use simplified ownership semantics: every guest process sees shared files as its own, and files it creates are owned by the host user. Permission bits stay meaningful on the host; ownership changes inside the guest have no effect on host files.

## Network

Each VM has exactly one network interface, and its only peer is its jailoc network broker on the host. The broker runs a userspace TCP/IP stack, serves DHCP and DNS, and decides every outgoing TCP connection and UDP flow before it opens a host socket:

| Destination | Decision |
|-------------|----------|
| Public IPv4 addresses | allowed |
| Addresses of any host interface, the broker gateway, the guest's own subnet | blocked |
| Loopback, link-local, multicast, reserved, cloud metadata endpoints | blocked |
| Private ranges (RFC 1918, CGNAT and other special-purpose ranges) | blocked unless covered by `allowed_networks` or a resolved `allowed_hosts` address |
| IPv6 | blocked |

Denied TCP connections are reset; denied UDP is dropped. DNS answers never change a decision: a public name that resolves to a private address is still blocked. Each decision is written to `broker.jsonl`.

No port is published on the host. An invalid policy file at start prevents the VM from starting. If the broker exits, the supervisor stops the VM, so the workspace never runs without its broker.

## Trade-offs

- **Start time:** a cached start takes about 7 seconds on Apple Silicon with the host Docker daemon and about 15 seconds with the builder VM, which has to boot first; the first start builds the image.
- **Resources:** each workspace reserves its own VM memory (`memory` + 1 GiB) and vCPUs (`cpu` rounded up); a build in the builder VM adds the builder (4 vCPUs, 4 GiB).
- **No Docker for the agent:** the workspace VM has no Docker daemon.
- **No remote mode:** sessions run OpenCode inside the VM; there is no OpenCode server port on the host.
- **Platform:** tested on macOS with libkrun. The broker is IPv4 only.

See the [threat model](threat-model.md) for what jailoc protects against in general, and [Run a workspace in a VM](../how-to/vm-backend.md) for setup.
