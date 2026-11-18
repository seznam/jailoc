# Security Boundaries

jailoc treats everything inside a workspace VM as hostile: the coding agent, every process it starts, guest root and the guest kernel. This page lists what jailoc trusts, the boundaries it enforces between trusted and untrusted parts, every channel that crosses them, and what lies outside the protection. [Threat Model](threat-model.md) explains why the boundaries are drawn this way.

## Trust levels

| Party | Trust | Why |
|-------|-------|-----|
| The host user running `jailoc` | Trusted | Owns the configuration, the shares and every host process jailoc starts |
| `config.toml`, `env_file`s and the files it references | Trusted | Decides what the VM may reach; `from_command` secrets and `[vm] runner` run as the host user |
| jailoc, `jailoc-guest` and the embedded VM runtime | Trusted | Release builds are attested; `jailoc-guest` is embedded in jailoc and always matches its version |
| Workspace images and Dockerfiles, including URL presets | Trusted to build, untrusted at run time | Build steps run with the builder's privileges; at run time the image is part of the guest |
| Everything inside a workspace VM | Untrusted | The agent processes untrusted content and can become root in the guest |
| Network destinations, DNS answers | Untrusted | DNS answers never open the network policy on their own |

## The boundaries

![Trust boundaries of one workspace: on the trusted host, the jailoc CLI reaches the guest over SSH through the network broker's socket, the broker sends policy-checked egress to the internet, and the VMM process (Landlock on Linux) serves host files. The untrusted workspace VM (agent, guest root, guest kernel) crosses the VM boundary only through virtio-net, whose only peer is the broker, and virtio-fs shares limited to declared directories.](img/security-boundaries.svg)

### VM and host

The hypervisor (KVM on Linux, Hypervisor.framework on macOS) separates the guest kernel from the host kernel. Guest root controls the guest and nothing else; everything it can reach on the host goes through devices the VMM provides:

- virtio-fs shares for workspace `paths`, `mounts` and jailoc's internal shares. Read-only shares are enforced by the VMM, so guest root can't remount them writable.
- One virtio-net interface whose only peer is the workspace's network broker.
- Unix sockets on the host that the broker forwards: the guest SSH server for jailoc, the host SSH agent with `ssh_auth_sock`, the host gpg-agent extra socket with `gpg_agent`, and `host_ports` targets.

#### Confining the VMM process

libkrun's virtio-fs server doesn't confine file requests to the shared directories by itself.

- **Linux.** jailoc runs the VMM process under Landlock with access only to:
    - the VM runtime,
    - `/dev/kvm` and the null and random devices,
    - the VM's root filesystem, data directory and disks,
    - the image's EROFS layer files (read-only),
    - the declared shares.

    jailoc refuses to start a VM when the kernel lacks Landlock. Landlock doesn't cover metadata operations such as `stat`, `chmod` or extended attributes.

- **macOS.** The VMM process runs without equivalent confinement.

#### What the host does with guest-written data

The host trusts nothing it reads back from the guest.

- The read-write internal share is writable by the guest, so it never holds keys or the VM specification.
- jailoc accesses that share only in ways that can't follow a link out of it.
- Per-workspace state directories on the host are created with mode `0700`.

### Guest and network

The network broker runs on the host, so nothing in the guest can change its policy. It terminates every guest connection in a userspace TCP/IP stack and opens a host socket only for destinations the policy allows: public addresses are allowed, private and reserved ranges are denied unless `allowed_networks` or a resolved `allowed_hosts` address opens them, and loopback, multicast, cloud metadata endpoints and special IPv6 ranges stay denied regardless. The guest has no network when the broker isn't running, and the VM stops when the broker exits. Containers from the in-VM Docker daemon go through the same broker. See [Network Isolation](network-isolation.md).

#### TLS inspection

With rules that have an `http` table, the broker also terminates TLS for the hosts they match.

- It signs certificates with a per-workspace CA whose key stays on the host, outside every share, and which the guest trusts, so the broker can read the decrypted traffic of those hosts.
- The upstream side is verified against the host's trust roots, not the guest's.
- WebSocket frames after an upgrade aren't inspected.

See [TLS inspection](network-isolation.md#tls-inspection).

### Workspace and workspace

Each workspace has its own VM, broker, SSH key, state directory and persistent home. A workspace doesn't see another workspace's files, secrets, sessions or network allowances, except through state the configuration shares between them:

| Shared state | Effect |
|--------------|--------|
| A `paths` or `mounts` directory used by several workspaces | Every workspace with the share can read and change it |
| A named agent profile | Logins, hooks, plugins and MCP settings carry across workspaces |
| A read-write agent configuration mount, such as `~/.config/opencode` mounted with `:rw` | The agent can plant configuration that runs in every workspace using the mount, and on the host |
| The shared builder VM | One build cache for every workspace that builds there; use `isolated_build` to separate it |

Workspaces that share a profile, a writable configuration mount or a directory share one trust level.

### Agent and guest root

Inside the VM the agent runs as `agent` (UID 1000) and can use `sudo` from the image. Guest root is not a boundary jailoc relies on, with one exception: `protect_git`. It does three things:

- sets `no_new_privs` in `jailoc-guest`, which every session inherits,
- binds `.git/hooks`, `.git/config` and the Git files they point to of repositories in the read-write `paths` and `mounts` read-only,
- rejects `enable_docker`, because Docker access is root access in the VM.

Without `protect_git`, the agent can rewrite Git hooks and configuration in shared repositories, and they run on the host the next time you use Git there.

### Image builds and host

Dockerfile `RUN` steps run where the image is built:

| Builder | Isolation of build steps |
|---------|--------------------------|
| Host Docker daemon (`builder = "docker"`, or `"auto"` with a usable daemon) | Container isolation only, with the host's full network access |
| Shared builder VM (`builder = "vm"`) | VM boundary, public addresses only, one cache for all workspaces |
| Per-workspace builder VM (`isolated_build`) | VM boundary, the workspace's network policy, its own cache |

Build a Dockerfile you don't trust only in a builder VM.

## Channels across the boundaries

Every way data or control crosses from the host into a workspace is opt-in or fixed by jailoc:

| Channel | Direction | What the guest can do with it | Configured by |
|---------|-----------|-------------------------------|---------------|
| Workspace `paths`, `rw` mounts | Both | Read, change and delete the files on the host | `paths`, `mounts` |
| `ro` mounts | Host to guest | Read the files | `mounts` |
| Snapshots, Git and agent configuration, `known_hosts` | Host to guest | Read a copy; nothing writes back | `snapshots`, `git_config`, `import_agent_config`, `ssh_auth_sock` |
| Secrets | Host to guest | Read every configured value | `secrets` |
| SSH agent | Guest to host | Ask the host agent to sign while forwarded; keys stay on the host | `ssh_auth_sock` |
| GPG agent | Guest to host | Ask the host gpg-agent to sign or decrypt with any key it holds while forwarded; the extra socket refuses key export and management; keys stay on the host | `gpg_agent` |
| `host_ports` | Guest to host | Use the host service like a local host process, outside the network policy | `host_ports` |
| `ports` | Host to guest | Serve on host `127.0.0.1` | `ports` |
| Web routes and desktop | Host to guest | Serve a guest port, or the desktop, to a browser that holds a login code, through `jailoc gateway` | `web`, `enable_desktop` |
| CA bundle | Host to guest | Trust the forwarded certificates | `ca_bundle` |
| Ingress CA | Host to browser | Not a jailoc channel: a CA you create and trust by hand, for the gateway's certificate | manual, see [Web gateway](#web-gateway) |
| Inspection CA | Host to guest | Trust certificates the broker signs for hosts matched by `http` rules, which lets the broker read their traffic | `network.rules` with `http` |
| Network egress | Guest to network | Reach public addresses and allowed private ones | `allowed_hosts`, `allowed_networks`, `network` |
| SSH session | Host to guest | Accept sessions only from jailoc, which authenticates with a per-workspace key and pins the guest host key generated at each start | fixed |
| Session notifications | Guest to host | Make jailoc show its fixed notification, `jailoc / <workspace>` with `The agent finished.` or `The agent needs your input.`, at most every 3 seconds and only while the terminal reports it is unfocused; never choose its text | `notifications` |

### host_ports needs the most care

A Chrome DevTools port or a container runtime socket gives the guest control of the browser or the host; jailoc warns about both.

### Web gateway

`jailoc gateway` exposes guest web services and the desktop to a browser on the host. The path has these properties:

- The gateway listens on the address you give it, normally host loopback, and serves only the routes of workspaces you name. It reaches a VM through a private Unix socket (`web.sock`, mode `0600`, in a state directory with mode `0700`), and the broker relays that to a fixed guest NIC port (6081). The relay forwards only to the loopback ports of configured routes, and no port is published on the host.
- A route is never open: access needs a one-time login code, printed by the gateway process, that expires 10 minutes after the gateway starts. The session cookie is `__Host-` prefixed and lasts 8 hours. Every request must match the route's `Host` and TLS server name exactly. A `GET` or `HEAD` needs no `Origin` header, but one that is present must be exactly the route's origin; every other method, and every WebSocket upgrade, requires it. Cross-site requests are refused.
- The browser can do whatever the service in the guest lets it do. The desktop gives whoever holds the login the agent's screen, keyboard and any program on it, which includes the agent's session and secrets in its environment. Treat a login as agent-level access.
- `web` routes can't target a `host_ports` guest port, so a route can't be used to reach a host service.
- Programs the desktop starts at boot get a snapshot of the environment without later secrets; programs an agent session launches onto the desktop inherit that session's environment, secrets included.

jailoc changes no host DNS, trust store or firewall. Setting up names, trust and the 443 redirect is manual, and it extends your host's trust:

- The ingress CA must be a dedicated one, separate from the network broker's inspection CA. Don't rely on a name constraint to limit it to `jailoc.local`: clients may ignore constraints on trust anchors, so a leaked CA key can mint certificates for any domain in every client that trusts the CA.
- The CA and leaf keys must stay outside every workspace `paths`, `mounts` and `snapshots` source. An agent that can read the CA key can issue certificates your browser accepts, for any domain where constraints are ignored.
- Redirect rules should match only `127.0.0.1` port 443, and DNS entries only the route names or `jailoc.local`. Browsers with a separate trust store, such as Firefox, need their own trust setup, and a browser with secure DNS may bypass the resolution configured on the host.
- The gateway doesn't support service workers. It rejects every service worker script fetch (`Service-Worker: script` or `Sec-Fetch-Dest: serviceworker`) with `403` before routing, login or proxying, and adds `worker-src 'none'` to every response as a separate `Content-Security-Policy` header, so an upstream policy can't weaken it. Both checks rely on headers a browser sets and don't constrain other clients. They stop new installs and updates only: a worker that a guest installed earlier on a reused, stable route origin stays registered in the browser, and can intercept requests to the gateway, including login codes. The gateway can't stop it server-side. Unregister every service worker and clear the site data for each development origin before you use the gateway on it; jailoc doesn't do that for you. Browser behaviour across restarts is unverified and needs a manual check before merge.
- A gateway serves the VM boots it started with. When a VM stops or is replaced, its routes close and a new gateway process must start.

The [how-to](../how-to/desktop-and-web.md) gives the commands and their cleanup.

### Session output is untrusted

Any process in the guest that writes to the session terminal can send the private reports of the OpenCode plugin, so the notifications prove nothing about what the agent did. jailoc decides only whether, when and with which fixed text and workspace name they appear.

The same process can also write terminal sequences of its own, including OSC 777 notifications with any text, which jailoc passes to the terminal like other agent output, whatever `notifications` says. How to tell a forged notification from a real one:

- A desktop notification from `"native"` comes from the host and can't be forged this way.
- With `"osc"`, a forged terminal notification looks like jailoc's.

## Outside the boundaries

jailoc doesn't protect against:

- **Exfiltration to the public internet.** Public egress is open, so anything the agent can read (shared source, secrets, snapshots) can leave the machine.
- **The agent reading what it's given.** Shared directories, snapshots and every configured secret are visible to the agent and to guest root.
- **Changes to shared directories.** Writes land on the host immediately; `protect_git` covers Git hooks and configuration only.
- **Services reached through `host_ports` or the SSH agent.** Their own access control is the only limit.
- **Bugs in the hypervisor, libkrun, the VMM process or the broker.** These run on the host as your user.
- **virtio-fs confinement on macOS**, and metadata operations on host files outside the shares on Linux, for a guest kernel under the agent's control.
- **Prompt injection itself.** jailoc limits what a manipulated agent can reach, not whether it gets manipulated.
- **Notifications OpenCode shows itself.** When OpenCode's own notifications are on, jailoc sends none, and OpenCode's follow its settings, not jailoc's focus check, fixed text or interval.
- **Resource exhaustion inside the VM.** `cpu` and `memory` cap the whole VM; nothing limits processes inside it.

## Release integrity

Release assets carry GitHub build provenance attestations and SPDX SBOMs, and every archive lists bundled components in `THIRD_PARTY_NOTICES`. Docker CE in the Docker layer is pinned and checksum-verified, and the builder VM image is pinned. See [Installation](../how-to/installation.md#check-the-runtime-licenses) for verifying a download.

## Reporting a vulnerability

Report vulnerabilities privately through [GitHub security advisories](https://github.com/seznam/jailoc/security/advisories/new), not in public issues. [SECURITY.md](https://github.com/seznam/jailoc/blob/main/SECURITY.md) describes what is in scope.
