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

![Trust boundaries of one workspace: on the trusted host, the jailoc CLI reaches the guest over SSH through the network broker's socket, the broker sends policy-checked egress to the internet, and the VMM process (Landlock on Linux, Seatbelt on macOS) serves host files. The untrusted workspace VM (agent, guest root, guest kernel) crosses the VM boundary only through virtio-net, whose only peer is the broker, and virtio-fs shares limited to declared directories.](img/security-boundaries.svg)

### VM and host

The hypervisor (KVM on Linux, Hypervisor.framework on macOS) separates the guest kernel from the host kernel. Guest root controls the guest and nothing else; everything it can reach on the host goes through devices the VMM provides:

- virtio-fs shares for workspace `paths`, `mounts` and jailoc's internal shares. Read-only shares are enforced by the VMM, so guest root can't remount them writable.
- One virtio-net interface whose only peer is the workspace's network broker.
- Unix sockets on the host that the broker forwards: the guest SSH server for jailoc, the host SSH agent with `ssh_auth_sock`, the host gpg-agent extra socket with `gpg_agent`, and `host_ports` targets.
- No virtio-vsock device: jailoc configures no vsock ports and attaches a virtio-net interface, so libkrun enables neither TSI (Transparent Socket Impersonation) nor host vsock endpoints; guest `AF_VSOCK` connections to the host fail with `ENODEV`.

#### Confining the VMM process

libkrun's virtio-fs server doesn't confine file requests to the shared directories by itself.

- **Linux.** jailoc runs the VMM process under Landlock with access only to:
    - the VM runtime,
    - `/dev/kvm` and the null and random devices,
    - the VM's root filesystem, data directory and disks,
    - the image's EROFS layer files (read-only),
    - the declared shares.

    jailoc refuses to start a VM when the kernel lacks Landlock. Landlock doesn't cover metadata operations such as `stat`, `chmod` or extended attributes.

- **macOS.** jailoc starts the VMM process under a Seatbelt profile that denies everything by default, on top of Apple's `system.sb` base. The profile grants:
    - read and execute access to the VM runtime,
    - read and write access to the VM's root filesystem, read-write disks, log files and read-write shares,
    - read-only access to read-only shares and read-only disks,
    - outbound Unix-socket connections only to the workspace's network broker socket.

    Unlike Landlock, Seatbelt also confines metadata operations such as `stat` outside the granted paths. jailoc refuses to start a VM when it can't apply the profile.

    !!! note "Seatbelt is deprecated, but here to stay for now"
        Apple has marked `sandbox_init`, the call that applies the profile, as deprecated since OS X 10.8 (2012), without shipping a replacement; App Sandbox entitlements can't express this profile model. Chromium, Codex CLI and Anthropic's sandbox runtime still rely on it, and Apple still uses Seatbelt internally. A future macOS release could remove or change it, which would end the VMM confinement on macOS.

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

A forwarded `ca_bundle` is baked into the image layers, so the images trust that CA for as long as they exist. The VM runtime keeps the image's own roots, so removing the bundle or `jailoc reload` doesn't revoke it, and a later layer can't remove it from the layers below; only a rebuild with `jailoc restart` produces an image without it. Image builds with a forwarded bundle always run in a builder VM, never on the host daemon, so jailoc changes no trust of the host Docker daemon or of the host. The bundle holds only the external certificates you configure, not the workspace inspection CA.

A guest that could write to a Dockerfile or build context could plant `RUN` steps or files that execute in the next build, on the host daemon or in a builder VM. jailoc therefore refuses to start a workspace when a read-write share of it or of a running workspace exposes a build input (a local Dockerfile or a build context, symlinks resolved), or when its own read-write shares expose the build inputs of any other configured workspace, running or not. Read-only shares of build inputs are allowed; jailoc never converts a share to read-only, and there is no opt-out. [Build inputs and read-write shares](../reference/image-resolution.md#build-inputs-and-read-write-shares) has the exact rules.

The check rejects configured guest write access to build inputs. It does not protect against host processes changing them, and it does not make files immutable. It knows the workspaces of the configuration in use and those that run or are starting; a workspace that only another configuration file defines is unknown while it is stopped. A guest that already changed a build input before the share was removed leaves the change in place, so check exposed files before the next build. Host bind mounts of directories inside a share and file systems mounted inside a share are not detected. A build context with more than 500,000 entries is refused.

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
| OAuth link handoff | Guest to host, then host to guest | Offer one authorization link through `xdg-open`, `$BROWSER` or the broker's `/register` endpoint; jailoc opens the link in the host browser automatically, with no prompt. The broker accepts only ports 1024-65535 and a valid URL. An ephemeral callback adds a single-use listener on host `127.0.0.1:<port>` (at most 8 pending, 3-minute lifetime) pinned to the exact `redirect_uri` path, requiring `code=` or `error=`, stripping browser request headers and answering with a fixed confirmation. A same-port configured or runtime-mapped forward is used as a raw TCP forward instead, without that ephemeral callback filtering, single-use, lifetime or pending limits | fixed |
| Proxy routes | Host to guest | Serve a guest port to a browser over HTTPS through the background proxy (with optional one-time login code when `auth = true`) | `proxy` |
| CA bundle | Host to guest | Trust the forwarded certificates, in the VM and in the images jailoc builds | `ca_bundle` |
| Proxy CA | Host to browser | Not a jailoc channel: a CA that jailoc generates and you trust by hand, for the proxy's certificate | manual, see [HTTPS proxy](#https-proxy) |
| Inspection CA | Host to guest | Trust certificates the broker signs for hosts matched by `http` rules, which lets the broker read their traffic | `network.rules` with `http` |
| Network egress | Guest to network | Reach public addresses and allowed private ones | `allowed_hosts`, `allowed_networks`, `network` |
| SSH session | Host to guest | Accept sessions only from jailoc, which authenticates with a per-workspace key and pins the guest host key generated at each start | fixed |
| Session notifications | Guest to host | Make jailoc show its fixed notification, `jailoc / <workspace>` with `The agent finished.` or `The agent needs your input.`, at most every 3 seconds and only while the terminal reports it is unfocused; never choose its text | `notifications` |

### host_ports needs the most care

A Chrome DevTools port or a container runtime socket gives the guest control of the browser or the host; jailoc warns about both.

### OAuth link handoff

A tool in the guest can ask for a browser login, and jailoc opens the browser on the host without asking you.

!!! warning
    The guest chooses the provider origin and the rest of the URL. jailoc opens any validated `http` or `https` URL that comes with an explicit loopback `redirect_uri`, with no allowlist, no provider trust check and no consent prompt. A compromised guest can therefore make your host browser load pages from any origin, with the logins that browser already has. jailoc doesn't verify the provider and can't tell whether a login succeeded.

The path has these properties:

- The normal `xdg-open` or `$BROWSER` wrapper opens a link only when it carries an explicit `http` loopback `redirect_uri` and a guest process listens on that port. These are wrapper checks, not host-enforced: any guest process can POST to the broker's `/register` endpoint, which doesn't prove an opener call or a listener. The broker validates the `http` or `https` URL, the loopback `redirect_uri` port and path and the characters it prints, and parses no terminal output.
- For an ephemeral callback the broker binds the host callback listener first (a same-port configured forward already listens) and then opens the exact captured URL with a fixed program, `/usr/bin/open` on macOS or `xdg-open` from `PATH` on Linux, started without a shell and with the desktop environment inherited from `jailoc up`. The guest opener exits 0 only when the host accepted the launch, which doesn't mean a window opened or the login succeeded.
- The ephemeral callback listener doesn't forward host cookies or other browser request headers into the guest. It strips them, passes only the callback path and query, and answers the browser with a fixed confirmation; the guest's response never reaches the browser.
- When the callback port is already a configured (`ports`) or runtime-mapped forward with the same host and guest port, the broker registers no callback and keeps that raw TCP forward. It still opens the link, but the single-use limit, the 3-minute lifetime, the header and body isolation and the pending link state don't apply: host cookies and request headers can reach the guest listener, and its response can reach the browser.
- `jailoc auth` prints or re-opens the link over a private Unix socket (`oauth.sock`, mode `0600`, in a state directory with mode `0700`) when the automatic launch fails. That API isn't reachable from the guest. The guest still knows every URL it generated itself.
- For an ephemeral callback the broker keeps the link only in jailoc-managed memory, bound to that callback, and clears it when the callback is claimed, expires, is replaced on the same port, is cancelled or the broker shuts down. A configured or runtime-mapped port has no pending link. Neither covers copies the guest made, such as its own logs or a URL it re-registers.
- Guest exec logs drop the query and fragment of recognized OAuth authorization URLs; other URLs and logs aren't redacted. Broker logs record only the callback host, port and path. The URL stays in host browser history, in the opener's argument list, which other processes of the same user can read, and in any terminal capture. jailoc can't erase it from there.

Automatic opening gives you no chance to review the link first, and jailoc adds no limit on which origin the guest may choose.

### HTTPS proxy

The HTTPS proxy, which `jailoc up` runs in the background for workspaces with `proxy` routes, exposes guest web services to a browser on the host. The path has these properties:

- The proxy listens on the address you give it, normally host loopback, and serves only the routes of workspaces you name. It reaches a VM through a private Unix socket (`proxy.sock`, mode `0600`, in a state directory with mode `0700`), and the broker relays that to a fixed guest NIC port (6081). Every workspace VM runs the relay, and it forwards only to the loopback ports of configured routes (none without routes); `jailoc reload` replaces that allowlist and closes connections through ports that left it. No port is published on the host.
- By default (`auth = false`), routes do not require a login code; setting `auth = true` gates the route behind a one-time login code (valid for 10 minutes) and an 8-hour `__Host-jailoc` session cookie. Upstream application cookies are rewritten to `__Host-jailoc-<host>-<name>` (`Path=/`, `Secure`, no `Domain`) and unwrapped on requests, and cookies without the route's prefix are dropped so a route cannot inject `Domain=jailoc.local` cookies into another route via `Set-Cookie` or `document.cookie`. Every request must match the route's `Host` and TLS server name exactly. A `GET` or `HEAD` needs no `Origin` header, but any present `Origin` or `Referer` must match the route's `https://<host>` origin; every other method, and every WebSocket upgrade, requires that exact `Origin`. Cross-site requests, cross-origin `nested-navigate` frames, and requests with mismatched `Sec-Fetch-Site` or `Referer` are refused, and every response carries `Cross-Origin-Resource-Policy: same-origin` and `Cross-Origin-Opener-Policy: same-origin`.
- The browser can do whatever the service in the guest lets it do.
- `proxy` routes can't target a `host_ports` guest port, so a route can't be used to reach a host service.
jailoc changes no host DNS, trust store or firewall. Setting up names, trust and the 443 redirect is manual, and it extends your host's trust:

- jailoc generates the proxy CA itself, in `~/.local/share/jailoc/proxy`, separate from the network broker's inspection CA, and offers no way to supply another certificate. The CA is a root and an intermediate. Both carry a critical name constraint that permits only names under `jailoc.local` and excludes every IP address, and jailoc refuses an existing CA without it. The root key signs the intermediate once and is never written to disk.
- The intermediate key stays on the host so jailoc can issue leaves for new workspaces. Clients may ignore name constraints on a trust anchor, but not on an intermediate in the chain, so a leaked intermediate key can mint certificates only for names under `jailoc.local`, which still lets it impersonate every route. Config validation rejects workspace `paths`, `mounts` and `snapshots` that contain or link to `~/.local/share/jailoc`. The proxy also refuses to use its CA when the directory overlaps a configured source or a share of a running VM, which keeps the shares it booted with until a restart; both sides are compared after resolving symlinks.
- Redirect rules should match only `127.0.0.1` port 443, and DNS entries only the route names or `jailoc.local`. Browsers with a separate trust store, such as Firefox, need their own trust setup, and a browser with secure DNS may bypass the resolution configured on the host.
- The proxy doesn't support service workers. It rejects every service worker script fetch (`Service-Worker: script` or `Sec-Fetch-Dest: serviceworker`) with `403` before routing, login or proxying, and adds `worker-src 'none'` to every response as a separate `Content-Security-Policy` header, so an upstream policy can't weaken it. Both checks rely on headers a browser sets and don't constrain other clients. They stop new installs and updates only: a worker that a guest installed earlier on a reused, stable route origin stays registered in the browser, and can intercept requests to the proxy, including login codes. The proxy can't stop it server-side. Unregister every service worker and clear the site data for each development origin before you use the proxy on it; jailoc doesn't do that for you. Browser behaviour across restarts is unverified and needs a manual check before merge.
- A proxy serves the VM boots it was given. When a VM stops or is replaced, its routes close until `jailoc up` or `jailoc restart` hands the new boot to the proxy.
- The optional `jailoc-listener-broker` is the only part that runs as root. It binds loopback port 443, parses no HTTP or TLS and holds no key; it passes each accepted TCP connection to the proxy over a socket that only the configured UID can open, and the proxy checks the socket belongs to root. Any process of that user can attach in the proxy's place, so it adds no separation between that user's processes. It refuses to run from a path that a non-root user can modify.

The [how-to](../how-to/proxy-routes.md) gives the commands and their cleanup.

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
- **Metadata operations on host files outside the shares on Linux**, for a guest kernel under the agent's control.
- **Prompt injection itself.** jailoc limits what a manipulated agent can reach, not whether it gets manipulated.
- **Notifications OpenCode shows itself.** When OpenCode's own notifications are on, jailoc sends none, and OpenCode's follow its settings, not jailoc's focus check, fixed text or interval.
- **Resource exhaustion inside the VM.** `cpu` and `memory` cap the whole VM; nothing limits processes inside it.

## Release integrity

Release assets carry GitHub build provenance attestations and SPDX SBOMs, and every archive lists bundled components in `THIRD_PARTY_NOTICES`. Docker CE in the Docker layer is pinned and checksum-verified, and the builder VM image is pinned. See [Installation](../how-to/installation.md#check-the-runtime-licenses) for verifying a download.

`jailoc self-update` checks the downloaded archive's SHA-256 against `checksums.txt` and the digest the GitHub API reports for the asset, before it parses the archive. It also requires the exact subject and the release workflow in the attestation metadata that the GitHub API reports. That metadata is an API response over HTTPS, not an independent cryptographic verification, so this check relies on trust in GitHub. When `gh` is installed, the updater runs `gh attestation verify` to verify the signature, timestamp, repository and release workflow, and any failure, including a `gh` too old to support it, stops the installation. Without `gh`, the updater warns that the attestation rests on what the GitHub API reports and that signatures were not verified locally. The 24-hour cooldown on new releases delays adoption; it does not prove a release is safe. See the [`jailoc self-update` reference](../reference/cli.md#jailoc-self-update).

## Reporting a vulnerability

Report vulnerabilities privately through [GitHub security advisories](https://github.com/seznam/jailoc/security/advisories/new), not in public issues. [SECURITY.md](https://github.com/seznam/jailoc/blob/main/SECURITY.md) describes what is in scope.
