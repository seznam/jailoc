# Configuration Reference

Configuration file location: `~/.config/jailoc/config.toml`

The file is auto-created with defaults on first run. All fields are optional unless noted.

---

## Top-level fields

The configuration has no settings of its own at the root level, only the tables `[vm]`, `[base]`, `[defaults]` and `[workspaces.<name>]`.

The jailoc v1 settings `mode` and `password_mode` are rejected at config load time. See [jailoc v1 settings](#jailoc-v1-settings).

---

## `[vm]`

Host runtime of the workspace VMs. All fields are optional.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runner` | string | (managed) | Path to a `go-microvm-runner` binary. When empty, jailoc downloads the pinned VM runtime (runner, `libkrun`, `libkrunfw`) for the platform on first use into `~/.cache/jailoc-vm/runtime/` and verifies its SHA-256. A pinned runtime exists for darwin/arm64 and linux/amd64; on other platforms `runner` and `lib_dir` are required. Must be absolute; supports `~` expansion. |
| `lib_dir` | string | (none) | Directory containing `libkrun` and `libkrunfw` for a configured `runner`. Ignored when `runner` is empty. When empty with a configured `runner`, jailoc uses `/opt/homebrew/lib` on macOS if it exists, otherwise the system library path. Must be absolute; supports `~` expansion. |
| `builder` | string | `"auto"` | Where workspace images are built: `"auto"` uses the host Docker daemon when it answers and runs Linux on the host's CPU architecture, otherwise the shared builder VM; `"docker"` requires the host daemon; `"vm"` always uses the shared builder VM. A workspace with `isolated_build = true` always uses its own builder VM. |

### Example

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/homebrew/lib"
```

---

## `[base]`

Global base image settings. Controls the base image used when no workspace or default `image` is set. See [Image Resolution](image-resolution.md).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for the base image. When set, takes priority over the embedded Dockerfile. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. |

### Example

```toml
[base]
dockerfile = "https://example.com/Dockerfiles/custom"
```

---

## `[defaults]`

Defaults applied to all workspaces.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend` | string | `"vm"` | Accepted values: `"vm"` or empty. `"docker"` is rejected as a jailoc v1 setting. |
| `image` | string | (none) | Pre-built image used as the base for workspace Dockerfile builds, or used directly when no workspace `dockerfile` is set. |
| `env` | string[] | `[]` | Environment variables for every session, in `KEY=VALUE` format. |
| `env_file` | string[] | `[]` | `.env` files loaded for all workspaces. Each file must exist at config load time. |
| `mounts` | string[] | `[]` | Additional directory shares, `host:container[:mode]`. See [`mounts`](#mounts). |
| `snapshots` | string[] | `[]` | Read-only snapshots of host files or directories. See [`snapshots`](#snapshots). |
| `allowed_hosts` | string[] | `[]` | Hostnames whose A and AAAA records are allowed through the network broker even when they resolve to private addresses. |
| `allowed_networks` | string[] | `[]` | IPv4 and IPv6 CIDR ranges allowed through the network broker. |
| `ports` | (int \| string)[] | `[]` | Guest ports published on host `127.0.0.1`. See [`ports`](#ports). |
| `ssh_auth_sock` | bool | `false` | Relay the host SSH agent (`$SSH_AUTH_SOCK`) into the VM at `/run/ssh-agent.sock` and copy `~/.ssh/known_hosts` into it. |
| `git_config` | bool | `true` | Copy the host Git configuration (`~/.gitconfig` or `~/.config/git/config`) read-only to `/home/agent/.gitconfig`. |
| `cpu` | float | `2.0` | vCPUs of the workspace VM, rounded up. |
| `memory` | string | `"4g"` | Memory of the workspace VM, at least `512m`. |
| `enable_docker` | bool | `true` | Run a Docker daemon inside the workspace VM. See [`enable_docker`](#enable_docker). |
| `protect_git` | bool | `false` | Make `.git/hooks` and `.git/config` of every Git repository under the workspace `paths` read-only inside the VM, and disable `sudo` and Docker there. |
| `isolated_build` | bool | `false` | Build the workspace image in a builder VM of its own, with its own build cache and the workspace's network policy, instead of the host Docker daemon or the shared builder VM. |
| `filtered_dns` | bool | `false` | Filter DNS answers in the network broker. Requires `dns_upstream` and at least one `dns_blocked_zones` entry. |
| `dns_upstream` | string | (none) | Numeric IPv4 resolver used for permitted DNS queries when `filtered_dns` is enabled. |
| `dns_blocked_zones` | string[] | `[]` | DNS suffixes answered with NXDOMAIN, including all subdomains. |
| `ca_bundle` | bool \| string | `true` | CA bundle added to the VM's system trust store. See [`ca_bundle`](#ca_bundle). |
| `secrets` | table | `{}` | Secrets for all workspaces. See [`secrets`](#secrets). |

### Example

```toml
[defaults]
image = "myregistry.example.com/myteam/opencode-base:v1.2.3"
env = ["GOPRIVATE=*.example.com"]
env_file = ["~/.config/jailoc/shared.env"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8", "fd12:3456::/48"]
ssh_auth_sock = true
cpu = 4.0
memory = "8g"
```

---

## `[workspaces.<name>]`

Each workspace is a TOML table under `[workspaces]`, keyed by name. Names must match `^[a-z0-9-]+$`.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `paths` | string[] | (required) | Host directories shared into the VM over virtio-fs at their original absolute paths. The first path is the working directory. |
| `backend` | string | (inherit) | `"vm"` or empty. `"docker"` is rejected as a jailoc v1 setting. |
| `image` | string | (none) | Pre-built image used directly, bypassing all build steps. Cannot be combined with `dockerfile` or `build_context`. |
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to an overlay Dockerfile built on top of the base image. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. |
| `build_context` | string | (none) | Build context of the overlay build. When empty, the Dockerfile's parent directory (local path) or a temporary directory (URL). |
| `mounts` | string[] | `[]` | Directory shares for this workspace, merged with `[defaults].mounts`. |
| `snapshots` | string[] | `[]` | Read-only snapshots, merged with `[defaults].snapshots`. |
| `allowed_hosts` | string[] | `[]` | Merged with `[defaults].allowed_hosts`. |
| `allowed_networks` | string[] | `[]` | Merged with `[defaults].allowed_networks`. |
| `ports` | (int \| string)[] | `[]` | Merged with `[defaults].ports`. |
| `env` | string[] | `[]` | Overrides `[defaults].env` entries with the same key. |
| `env_file` | string[] | `[]` | Loaded after `[defaults].env_file`. |
| `secrets` | table | `{}` | Overrides default secrets with the same name. |
| `ssh_auth_sock`, `git_config`, `cpu`, `memory`, `enable_docker`, `protect_git`, `isolated_build`, `filtered_dns`, `dns_upstream`, `dns_blocked_zones`, `ca_bundle` | | (inherit) | Same meaning as in `[defaults]`; the workspace value wins. |

### Example

```toml
[workspaces.my-project]
paths = ["~/projects/my-project", "~/shared/libs"]
allowed_hosts = ["api.example.com"]
snapshots = ["~/.npmrc:/home/agent/.npmrc"]
ports = [3000, "8080:80"]
dockerfile = "~/projects/my-project/overlay.Dockerfile"
```

---

## Validation Rules

### jailoc v1 settings

These settings fail config load with ``<setting> is a jailoc v1 setting; run `jailoc migrate` to update the config``:

- `backend = "docker"` in `[defaults]` or a workspace
- top-level `mode` and `password_mode`
- `expose_port` in `[defaults]` or a workspace

Any `backend` value other than `"vm"` and empty is rejected. See [Migrate from jailoc v1](../how-to/migrate-from-v1.md).

### `paths`

`~` is expanded. Empty entries are rejected. Paths equal to or under these prefixes are rejected: `/home/agent`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/opt`, `/root`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/certs`. A path configured in more than one workspace prints a warning.

### `mounts`

Format: `host:container[:mode]`, mode `ro` or `rw` (default `rw`). Mounts are virtio-fs directory shares.

- `host`: absolute or `~` path. Must be a directory; a file mount fails at `jailoc up`. Use [`snapshots`](#snapshots) for files. An empty host (`":/home/agent/.opencode"`) removes an inherited mount with that destination.
- `container`: absolute after `~` expansion. Rejected when equal to or under `/`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/opt`, `/root`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/certs`. Destinations under `/home/agent/...` are allowed.

Host paths equal to or under these entries are rejected: `/`, `/boot`, `/dev`, `/etc`, `/private`, `/proc`, `/sys`, `/run`, `/var`, `~/.ssh`, `~/.gnupg`, `~/.aws`.

### `snapshots`

Format: `host:container`, optionally `:ro`; `:rw` is rejected because snapshots are read-only. Host and destination rules are the same as for `mounts`, except that the host may be a file or a directory. An empty host removes an inherited snapshot.

jailoc copies the sources when the VM starts and again on every `jailoc reload`, which also applies added and removed entries. At `jailoc up` and `jailoc reload`, the destination must not equal, contain or lie inside a shared path (workspace `paths`, `mounts` destinations, the persistent home directories), and the source must exist.

### `ports`

Each entry is a port number (`3000`: guest 3000 on host 3000) or `"HOST:GUEST"` (`"8080:80"`: guest 80 on host 8080), with ports 1 to 65535. A host port may appear only once per list. A workspace entry replaces a `[defaults]` entry with the same host port. Ports are published on host `127.0.0.1` only.

### `allowed_hosts` and `allowed_networks`

`allowed_networks` entries must parse with Go's `net.ParseCIDR`; IPv4 and IPv6 CIDRs are accepted. `allowed_hosts` entries are resolved on the host for A and AAAA records, and each address is allowed as a `/32` or `/128`. Some IPv6 ranges stay denied regardless of these lists; see [Network isolation](../explanation/network-isolation.md).

### Filtered DNS

When `filtered_dns = true`, an effective `dns_upstream` and at least one effective `dns_blocked_zones` entry are required. `dns_upstream` must be a numeric IPv4 address other than unspecified, loopback, link-local or multicast. Each zone must be a DNS name of labels with letters, digits or internal hyphens (63 characters per label, 253 total); a trailing dot is accepted and `"."` is rejected. A and AAAA answers for blocked zones are filtered, matched case-insensitively including subdomains. Direct DNS to port 53 of other servers is blocked.

With filtering on, an effective `allowed_hosts` entry equal to or under an effective blocked zone is rejected at config load.

### `ca_bundle`

- Omitted or `true`: automatic discovery. The non-empty `SSL_CERT_FILE` environment variable is used first, then `NIX_SSL_CERT_FILE`. If the selected variable points at an invalid source, `jailoc up` fails without trying the next one. If neither is set, no bundle is forwarded.
- `false`: no bundle.
- Non-empty string: custom bundle path, absolute or `~`. Symlinks are resolved; the target must be a readable regular file with at least one `CERTIFICATE` PEM block and no private-key PEM block.
- Empty string or any other TOML type: rejected at config load.

The bundle is added to the VM's system trust store, which its Docker daemon also uses, and to the builder VM's Docker daemon. Sessions get `NODE_USE_SYSTEM_CA=1` when a bundle is forwarded.

### `enable_docker`

With `enable_docker = true` (the default), jailoc starts `dockerd` inside the workspace VM when the image contains `dockerd`. Without `dockerd`, an explicit `enable_docker = true` fails at `jailoc up` and the default only prints a warning. `enable_docker = true` together with `protect_git = true` is rejected, because Docker access is root access inside the VM; `protect_git` alone turns Docker off.

### `cpu` and `memory`

`cpu` must be a finite number greater than 0. `memory` must match `^[1-9][0-9]*[kmgKMG]?$` (for example `512m`, `4g`) and be at least `512m` at `jailoc up`.

### `image`, `dockerfile`, `build_context`

`image` cannot be combined with `dockerfile` or `build_context`, and must not be whitespace-only. `dockerfile` accepts absolute paths, `~` paths and `http`/`https` URLs with a non-empty host (`http:///path` is rejected). Relative paths and other schemes are rejected.

### `env`

Each entry must be `KEY=VALUE` with a non-empty key. Reserved keys, which jailoc sets itself: `JAILOC`, `JAILOC_WORKSPACE`, `NODE_USE_SYSTEM_CA`, `OPENCODE_TUI_CONFIG`, `SSH_AUTH_SOCK`.

### `env_file`

Paths must be absolute or start with `~`, and the files must exist at config load time. Format: `KEY=VALUE` lines, `#` comments, empty lines ignored, quoted values allowed, inline comments stripped. Values are literal; no host variable expansion. Entries are checked against the reserved keys.

### `secrets`

Secrets live under `[defaults.secrets.env.<NAME>]`, `[defaults.secrets.file.<NAME>]`, `[workspaces.<ws>.secrets.env.<NAME>]` or `[workspaces.<ws>.secrets.file.<NAME>]`. The sub-table sets the destination, the field sets the source.

| Field | Type | Description |
|---|---|---|
| `from_env` | string | Host environment variable to read. Must match `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `from_file` | string | Host file to read. Absolute or `~` path. |
| `from_command` | string | Command run on the host with `/bin/sh -c`; its standard output is the value. Must not be blank. |

Exactly one of `from_env`, `from_file` and `from_command` must be set.

| Destination | `<NAME>` rule | In the guest |
|---|---|---|
| `secrets.env.<NAME>` | `^[A-Za-z_][A-Za-z0-9_]*$`; not `HOME`, `PATH` or a reserved `env` key | exported as `<NAME>` in session environments |
| `secrets.file.<NAME>` | `^[a-zA-Z0-9_-]+$` | `/run/secrets/<NAME>` |

A name may not appear in both `secrets.env` and `secrets.file` of the same scope.

At `jailoc up`, `jailoc reload` and `jailoc add`, jailoc reads every source on the host: a `from_env` variable must be set and non-empty, a `from_file` path must be a regular file, and a `from_command` must exit with status 0, print something and finish within two minutes. Commands run with the terminal attached. Env secret values lose trailing `\r` and `\n` characters and must not contain line breaks or NUL. Values travel to the guest over SSH; file secrets are written to `/run/secrets` (tmpfs, mode `0400`, owned by the agent) and env secrets go to the session environment only. jailoc never writes them to host disk. `jailoc reload` re-reads and re-pushes secrets and removes files of secrets no longer configured; running sessions keep their old environment. See the [secrets how-to](../how-to/secrets.md).

---

## Merge Semantics

Environment variables merge in this order, later wins per key:

1. `[defaults].env`
2. `[defaults].env_file` (in list order)
3. workspace `env_file` (in list order)
4. workspace `env`

`allowed_hosts` and `allowed_networks` are unions of the defaults and workspace lists, deduplicated in order.

`mounts` merges three layers keyed by destination: built-in default mounts, `[defaults].mounts`, workspace `mounts`. The last layer to set a destination wins; an empty host removes it. Built-in default mounts:

| Destination | Host source | Mode |
|---|---|---|
| `/home/agent/.config/opencode` | `~/.config/opencode` | `rw` |
| `/home/agent/.opencode` | `~/.opencode` | `rw` |
| `/home/agent/.claude/transcripts` | `~/.claude/transcripts` | `rw` |
| `/home/agent/.agents` | `~/.agents` | `ro` |

`snapshots` merges `[defaults].snapshots` and workspace `snapshots` keyed by destination. `git_config` adds a lowest-priority snapshot of `/home/agent/.gitconfig`, which a `snapshots` entry for that destination replaces or, with an empty host, removes.

`ports` merges by host port; the workspace entry wins.

`secrets` merges by name. A workspace secret replaces a default secret with the same name entirely, also across `env` and `file`. An inherited default secret can't be unset in a workspace.

Scalar settings (`ssh_auth_sock`, `git_config`, `cpu`, `memory`, `enable_docker`, `protect_git`, `isolated_build`, `filtered_dns`, `ca_bundle`) take the workspace value when set, otherwise `[defaults]`, otherwise the built-in default. An empty workspace `dns_upstream` and an omitted `dns_blocked_zones` inherit the defaults; a set `dns_blocked_zones` replaces the default list.

---

## Persistent state

`~/.cache/jailoc-vm/workspaces/<workspace>/home` keeps the agent's `~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache` across restarts. `jailoc down --purge` deletes it.

---

See the [workspace configuration how-to](../how-to/workspace-configuration.md) for step-by-step setup.
