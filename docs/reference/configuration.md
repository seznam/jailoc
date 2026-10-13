# Configuration Reference

Configuration file location: `~/.config/jailoc/config.toml`

The file is auto-created with defaults on first run. All fields are optional unless noted.

---

## Top-level fields

Fields set at the root level of `config.toml`, outside any TOML table.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `password_mode` | string | `"auto"` | Password storage mode. Accepted values: `"auto"` (env var override → OS keyring → password file), `"env"` (env var only — error if unset), `"keyring"` (OS keyring only — error if unavailable), `"file"` (file only, skips keyring). |

jailoc auto-generates a 64-character hex password on first run and stores it in the OS keyring (preferred) with a fallback file at `~/.local/share/jailoc/{workspace}/password`. When the keyring is used, a marker file is written instead of the actual password. If the keyring becomes unavailable later, jailoc reports an error with the marker file path — delete it to generate a new file-based password. `jailoc status` shows the source label (`env`, `keyring`, or `file`).

### Example

```toml
password_mode = "auto"
```

---

## `[base]`

Global base image settings. Controls the fallback image used when no workspace-level `image` is set.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for the base image. When set, takes priority over the embedded fallback. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. Supports `~` expansion for local paths. |

### Example

```toml
[base]
dockerfile = "https://example.com/Dockerfiles/custom"
```

Or with a local Dockerfile:

```toml
[base]
dockerfile = "/opt/myorg/base.Dockerfile"
```

---

## `[defaults]`

Global defaults applied to all workspaces. All fields are optional and default to empty.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `image` | string | (none) | Pre-built Docker image used as the base for workspace Dockerfile builds, or pulled directly when no workspace `dockerfile` is set. |
| `env` | string[] | `[]` | Environment variables applied to all workspaces. Each entry must be in `KEY=VALUE` format. Workspace `env` entries take precedence over defaults with the same key. |
| `env_file` | string[] | `[]` | Paths to `.env` files loaded for all workspaces. Each file must exist at config load time. Paths must be absolute (`/...`) or start with `~`. Parsed before workspace-level `env_file` entries. |
| `mounts` | string[] | `[]` | Additional bind mounts for all workspaces. Each entry follows `host:container[:mode]` format where mode is `ro` or `rw` (default `rw`). An empty host source removes a default mount matching the container path. Merged with per-workspace `mounts` — see Merge Semantics below. Supports `~` expansion on both host and container sides. |
| `allowed_hosts` | string[] | `[]` | Hostnames allowed through the firewall for all workspaces. Merged with per-workspace `allowed_hosts`. |
| `allowed_networks` | string[] | `[]` | CIDR ranges allowed through the firewall for all workspaces. Merged with per-workspace `allowed_networks`. |
| `ssh_auth_sock` | bool | `false` | Mount the host SSH agent socket into the container. Auto-detects the socket: Docker Desktop/OrbStack magic path first, then `$SSH_AUTH_SOCK`. Also mounts `~/.ssh/known_hosts` read-only when enabled. |
| `git_config` | bool | `true` | Mount the host Git configuration (`~/.gitconfig` or `~/.config/git/config`) read-only into the container. |
| `expose_port` | bool | `true` | Expose the opencode container port to the host. When `false`, the `ports:` section is omitted from the generated compose file and the workspace is only accessible via `jailoc --exec`. |
| `cpu` | float64 | `2.0` | Number of CPU cores allocated to the opencode container. Must be greater than 0. |
| `memory` | string | `"4g"` | Memory limit for the opencode container. Accepts Docker memory format: a positive integer optionally followed by `k`, `m`, or `g` suffix (e.g. `512m`, `4g`, `1024`). Must be greater than 0. |
| `enable_docker` | bool | `true` | Start a Docker-in-Docker sidecar alongside the opencode container. When `false`, the dind service, TLS certificate volumes, and `DOCKER_HOST`/`DOCKER_TLS_*` environment variables are omitted from the generated compose file, so Docker access from inside the container is unavailable by default and `docker` commands will not be able to connect to a daemon. Disabling reduces resource overhead and tightens security when the agent does not need Docker. |
| `filtered_dns` | bool | `false` | Route DNS for opencode and dind through a CoreDNS sidecar. Requires `dns_upstream` and at least one `dns_blocked_zones` entry. |
| `dns_upstream` | string | (none) | Numeric IPv4 resolver used for permitted DNS queries when `filtered_dns` is enabled. Hostnames, loopback, link-local, multicast, and unspecified addresses are rejected. |
| `dns_blocked_zones` | string[] | `[]` | DNS suffixes blocked by the sidecar with NXDOMAIN, including all subdomains. At least one zone is required when `filtered_dns` is enabled. |
| `ca_bundle` | bool \| string | `true` | Controls the CA bundle forwarded into the opencode container and, when enabled, the DinD daemon. Omitted or `true` enables automatic discovery: the non-empty `SSL_CERT_FILE` environment variable is used first, then the non-empty `NIX_SSL_CERT_FILE` environment variable; if neither is set, no bundle is forwarded. `false` disables forwarding. A string selects a custom bundle file (absolute path or `~`-prefixed). See [Network access: forward a CA bundle](../how-to/network-access.md#forward-a-ca-bundle) and [Container Architecture: CA bundle trust](../explanation/container-architecture.md#ca-bundle-trust). |
| `secrets` | table | `{}` | Secrets configuration map applied to all workspaces. See [secrets](#secrets) validation rules and the [secrets how-to](../how-to/secrets.md). |

### Example

```toml
[defaults]
image = "myregistry.example.com/myteam/opencode-base:v1.2.3"
env = ["GOPRIVATE=*.example.com", "NPM_REGISTRY=https://npm.example.com"]
env_file = ["~/.config/jailoc/shared.env"]
mounts = ["~/.local/share/opencode:/home/agent/.local/share/opencode"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8"]
ssh_auth_sock = true
git_config = true
enable_docker = true
cpu = 4.0
memory = "8g"
```

---

## `[workspaces.<name>]`

Each workspace is declared as a TOML table under `[workspaces]`, keyed by name.

**Workspace name constraints:** must match `^[a-z0-9-]+$` (lowercase letters, digits, and hyphens only).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `paths` | string[] | (required) | Directories bind-mounted into the container at their original absolute paths. The first path becomes the container's working directory. Supports `~` expansion. |
| `mounts` | string[] | `[]` | Bind mounts for this workspace. Format: `host:container[:mode]` — mode is `ro` or `rw` (default `rw`). An empty host source (e.g. `":/home/agent/.opencode"`) removes a default mount matching the container path. Container destinations under `/home/agent/...` are allowed (unlike `paths`); see [mounts validation](#mounts) for the full list of forbidden container prefixes. |
| `allowed_hosts` | string[] | `[]` | Hostnames resolved at container start and added as iptables `ACCEPT` rules before the private-range `DROP` rules. |
| `allowed_networks` | string[] | `[]` | CIDR ranges explicitly allowed through the container firewall. |
| `image` | string | (none) | Pre-built Docker image to use directly for this workspace, bypassing all build steps. Compose pulls the image natively at startup. Cannot be combined with `dockerfile` or `build_context`. |
| `build_context` | string | (none) | Docker build context directory for the workspace overlay build. When empty and `dockerfile` is a local path, defaults to the parent directory of the Dockerfile. When empty and `dockerfile` is an HTTP URL, a temporary directory is used. Supports `~` expansion. |
| `mode` | string | `""` | Connection mode for `jailoc`. Accepted values: `"remote"`, `"exec"`, `""` (auto-detect). |
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for a workspace-specific overlay image. Builds on top of the base image resolved by `[base]` settings. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. Supports `~` expansion for local paths. |
| `env` | string[] | `[]` | Environment variables for this workspace. Each entry in `KEY=VALUE` format. These override any global `defaults.env` entry with the same key. Reserved keys are rejected (see Validation Rules). |
| `env_file` | string[] | `[]` | Paths to `.env` files for this workspace. Each file must exist at config load time. Paths must be absolute (`/...`) or start with `~`. Loaded after global `defaults.env_file` entries. |
| `ssh_auth_sock` | bool | (inherit) | Mount the host SSH agent socket into the container. When not set, inherits from `[defaults]`. Also mounts `~/.ssh/known_hosts` read-only when enabled. |
| `git_config` | bool | (inherit) | Mount the host Git configuration read-only into the container. When not set, inherits from `[defaults]`. Falls back to `true` when neither the workspace nor defaults set it. |
| `expose_port` | bool | (inherit) | Expose the opencode container port to the host. When not set, inherits from `[defaults]`. Falls back to `true` when neither the workspace nor defaults set it. When `false`, `jailoc status` shows "not exposed (exec-only)" instead of the port number. |
| `cpu` | float64 | (inherit) | Number of CPU cores allocated to the opencode container. When not set, inherits from `[defaults]`. Falls back to `2.0` when neither the workspace nor defaults set it. |
| `memory` | string | (inherit) | Memory limit for the opencode container. When not set, inherits from `[defaults]`. Falls back to `"4g"` when neither the workspace nor defaults set it. |
| `enable_docker` | bool | (inherit) | Start a Docker-in-Docker sidecar for this workspace. When not set, inherits from `[defaults]`. Falls back to `true` when neither the workspace nor defaults set it. When `false`, the dind service, TLS certificate volumes, and `DOCKER_HOST`/`DOCKER_TLS_*` environment variables are omitted. |
| `filtered_dns` | bool | (inherit; `false`) | Override the default filtered DNS setting for this workspace. `false` disables the DNS sidecar even if enabled in `[defaults]`. |
| `dns_upstream` | string | (inherit) | Override the numeric IPv4 upstream resolver for this workspace. |
| `dns_blocked_zones` | string[] | (inherit) | Override the default blocked DNS suffixes for this workspace. |
| `secrets` | table | `{}` | Secrets configuration map for this workspace. Overrides default secrets with the same secret name. See [secrets](#secrets) validation rules and the [secrets how-to](../how-to/secrets.md). |
| `ca_bundle` | bool \| string | (inherit) | Controls the CA bundle forwarded into this workspace's opencode container and optional DinD daemon. When not set, inherits from `[defaults]`. Falls back to `true` (automatic discovery) when neither the workspace nor defaults set it. `false` disables forwarding for this workspace; a string overrides with a custom bundle path. See [Network access: forward a CA bundle](../how-to/network-access.md#forward-a-ca-bundle). |

!!! note
    `image` is mutually exclusive with `dockerfile` and `build_context`. Setting `image` alongside either of those fields is a validation error.

### Example

```toml
[workspaces.my-project]
paths = ["~/projects/my-project", "~/shared/libs"]
allowed_hosts = ["api.example.com", "pypi.org"]
allowed_networks = ["10.0.0.0/8"]
mounts = ["~/.local/share/opencode:/home/agent/.local/share/opencode"]
build_context = "~/projects/my-project/docker"
mode = "remote"
dockerfile = "~/projects/my-project/overlay.Dockerfile"
```

---

## Validation Rules

### Workspace names

Must match the regular expression `^[a-z0-9-]+$`. Names containing uppercase letters, underscores, or other special characters are rejected.

### `paths`

Each entry is validated against a list of forbidden path prefixes. Paths starting with any of the following are rejected:

| Forbidden prefix |
|-----------------|
| `/home/agent` |
| `/usr` |
| `/etc` |
| `/var` |
| `/bin` |
| `/sbin` |
| `/lib` |
| `/lib64` |

`~` is expanded to `$HOME` before validation. HTTP(S) URLs in `dockerfile` fields are not subject to `~` expansion; local paths starting with `~` are expanded.

### `mounts`

Each entry uses the format `host:container[:mode]`.

- `host`: a local path (absolute or starting with `~`). An empty string signals removal of a default mount matching the container path.
- `container`: must be an absolute path after `~` expansion. Validated against a separate set of forbidden prefixes: `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/opt`, `/root`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/certs`. Unlike `paths`, destinations under `/home/agent/...` are allowed.
- `mode`: `ro` or `rw`. Defaults to `rw` when omitted.

The following host paths are forbidden:

| Forbidden host path |
|---|
| `/` |
| `/boot` |
| `/dev` |
| `/etc` |
| `/private` |
| `/proc` |
| `/sys` |
| `/run` |
| `/var` |
| `~/.ssh` |
| `~/.gnupg` |
| `~/.aws` |

Any host path equal to or starting with these prefixes is rejected.

### `allowed_networks`

Each entry must be a valid CIDR notation string as accepted by Go's `net.ParseCIDR`. Invalid CIDR values are rejected at config load time.

### Filtered DNS

When `filtered_dns = true`, an effective `dns_upstream` and at least one effective `dns_blocked_zones` entry are required. `dns_upstream` must be a numeric IPv4 address other than unspecified, loopback, link-local, or multicast. Each blocked zone must be a DNS name made of labels containing letters, digits, or internal hyphens (maximum 63 characters per label and 253 characters total); a trailing dot is accepted. A root zone (`"."`) is rejected. An empty workspace `dns_upstream` inherits the default; an omitted workspace zone list inherits the default list. Zones are matched case-insensitively, including their subdomains, and receive NXDOMAIN for all DNS query types. Other queries go to the configured upstream.

When filtering is enabled, each workspace's effective `allowed_hosts` entries (defaults plus workspace) must fall outside its effective `dns_blocked_zones`. An exact match or subdomain match, ignoring case and a trailing dot, is rejected at config load with the workspace, host, and zone named in the error. A workspace zone list replaces the defaults; `filtered_dns = false` skips this cross-check.

The setting controls ordinary port-53 resolution, not DNS over HTTPS, other encrypted DNS protocols, proxies, or direct IP access. It does not replace the private-network firewall.

### `ca_bundle`

Accepted values:

- **Omitted in `[defaults]`**: automatic discovery.
- **Omitted in a workspace**: inherits `[defaults]`; if both are omitted, automatic discovery applies.
- **`true`**: automatic discovery. Checks the process environment variable `SSL_CERT_FILE` first (must be non-empty), then `NIX_SSL_CERT_FILE`. If the selected higher-priority variable points at an invalid source, `jailoc up` or a running-workspace `jailoc add` restart fails — there is no fallback to the next variable. If neither variable is set, no bundle is forwarded and no error occurs.
- **`false`**: disables forwarding for the scope where it is set.
- **Non-empty string**: a custom bundle path. Must be absolute (`/...`) or start with `~` (expanded to `$HOME`). Must not contain `$`. Symlinks are resolved; the resolved target must be a readable regular file containing at least one `CERTIFICATE` PEM block and no private-key PEM block.
- **Empty string**: rejected at config load time.
- Any other TOML type (integer, array, table): rejected at config load time.

The selected bundle is installed in the opencode container even when `enable_docker` resolves to `false`. When Docker is enabled, the same materialized bundle is also installed in DinD. Disabled forwarding and automatic discovery without a source remove any stale materialized bundle.

### Workspace `image`

The workspace `image` field is mutually exclusive with `dockerfile` and `build_context`. The following combinations are rejected at config load time:

- `image` set together with `dockerfile`
- `image` set together with `build_context`

### `cpu`

Must be a positive number (greater than 0). Applies to both `[defaults]` and workspace sections. Zero and negative values are rejected at config load time.

### `memory`

Must match the regular expression `^[1-9][0-9]*[kmgKMG]?$` — a positive integer optionally followed by a `k`, `m`, or `g` suffix (case-insensitive). Examples: `512m`, `4g`, `1024`. Values like `0`, `0m`, or empty strings are rejected at config load time.

### `dockerfile` fields

Accepted values:

- **Absolute local paths**: must start with `/` (e.g. `/opt/my.Dockerfile`)
- **Tilde paths**: must start with `~` (e.g. `~/my.Dockerfile`); `~` is expanded to `$HOME` before use
- **HTTP(S) URLs**: must have an `http` or `https` scheme and a non-empty host component. Paths like `http:///path` are rejected.

Relative paths and other URL schemes (e.g. `ftp://`, `file://`) are not accepted.

### `env`

Each entry must be in `KEY=VALUE` format (key cannot be empty, must contain `=`). The following keys are reserved and cannot be set by users:

| Reserved key | Reason |
|---|---|
| `OPENCODE_LOG` | opencode runtime config |
| `OPENCODE_SERVER_PASSWORD` | managed automatically by jailoc; set to override the automatic password cascade |
| `NODE_USE_SYSTEM_CA` | managed automatically when `ca_bundle` is active |
| `DOCKER_HOST` | DinD TLS connection |
| `DOCKER_TLS_CERTDIR` | DinD TLS certs |
| `DOCKER_CERT_PATH` | DinD TLS certs |
| `DOCKER_TLS_VERIFY` | DinD TLS verification |
| `SSH_AUTH_SOCK` | SSH agent passthrough |
| `JAILOC_FILTERED_DNS` | filtered DNS firewall selection |

### `env_file`

Each path must be absolute (starting with `/`) or start with `~` (expanded to `$HOME`). Files must exist at config load time. The file format is Docker `.env` compatible:

- Lines in `KEY=VALUE` format
- Lines starting with `#` are comments
- Empty lines are ignored
- Values may be quoted (`KEY="value with spaces"`)
- Inline comments after values are stripped

Values are treated as literal strings — no host environment variable expansion is performed.

### `secrets`

Secrets are configured under `[defaults.secrets.env.<NAME>]`, `[defaults.secrets.file.<NAME>]`, `[workspaces.<ws>.secrets.env.<NAME>]`, or `[workspaces.<ws>.secrets.file.<NAME>]`. Secrets declared at the top level outside `[defaults]` or `[workspaces.<ws>]` are rejected.

Secret configuration is defined by two independent choices:
- **Destination** (`env` vs `file` sub-table): Determines how the secret is exposed inside the container.
  - `secrets.env.<NAME>` exports the secret value as a container environment variable named `<NAME>`.
  - `secrets.file.<NAME>` mounts the secret as a file at `/run/secrets/<NAME>` inside the container and never exports it as an environment variable.
- **Source** (`from_env` vs `from_file` field): Determines where the secret value is read from on the host.
  - `from_env` reads from a host environment variable.
  - `from_file` reads from a host file path.

#### Destination × Source Matrix

Destination and source combine independently into four valid configurations:

| Combination | Destination sub-table | Source field | Behavior inside container | Host permission requirements |
|---|---|---|---|---|
| 1 | `secrets.env.<NAME>` | `from_env` | Exported as env var `<NAME>` | Host env var must be set and non-empty |
| 2 | `secrets.env.<NAME>` | `from_file` | Exported as env var `<NAME>` | Host file must exist; read by root entrypoint |
| 3 | `secrets.file.<NAME>` | `from_env` | Mounted at `/run/secrets/<NAME>` (0444) | Host env var must be set and non-empty |
| 4 | `secrets.file.<NAME>` | `from_file` | Mounted at `/run/secrets/<NAME>` | Host file must exist and be world-readable (`o+r`) |

#### Fields

Each secret entry under `secrets.env.<NAME>` or `secrets.file.<NAME>` accepts the following fields. Exactly one source field (`from_env` or `from_file`) must be set per secret entry:

| Field | Type | Default | Description |
|---|---|---|---|
| `from_env` | string | (optional) | Host environment variable name to read the secret value from. Must match `^[A-Za-z_][A-Za-z0-9_]*$`, which excludes `$` for the same Docker Compose interpolation reason as `from_file`. Must not be empty. Mutually exclusive with `from_file`. |
| `from_file` | string | (optional) | Host file path to read the secret from. Must be absolute (`/...`) or start with `~` (expanded to home directory). Must not contain `$` (Docker Compose interpolation constraint). Must not be empty. Mutually exclusive with `from_env`. |

#### Environment destination (`secrets.env.<NAME>`)

Environment-destination secrets export secret values into the container environment as `<NAME>`, regardless of whether the source is `from_env` or `from_file`. Every environment-destination secret is unconditionally exported as a container environment variable.

##### `<NAME>` Constraints
The container environment variable name (`<NAME>`) must match `^[A-Za-z_][A-Za-z0-9_]*$`. The following names are reserved and rejected:
- `HOME`
- `PATH`
- `OPENCODE_LOG`
- `OPENCODE_SERVER_PASSWORD`
- `NODE_USE_SYSTEM_CA`
- `DOCKER_HOST`
- `DOCKER_TLS_CERTDIR`
- `DOCKER_CERT_PATH`
- `DOCKER_TLS_VERIFY`
- `SSH_AUTH_SOCK`
- `JAILOC`
- `JAILOC_WORKSPACE`
- `JAILOC_FILTERED_DNS`

#### File destination (`secrets.file.<NAME>`)

File-destination secrets mount secret values into the container at `/run/secrets/<NAME>`. File-destination secrets are never exported as container environment variables, regardless of whether the source is `from_env` or `from_file`.

##### `<NAME>` Constraints
The secret file name (`<NAME>`) must match `^[a-zA-Z0-9_-]+$`.

#### Source Validation at Up-Time

When `jailoc up` or `jailoc add` runs, secret sources are validated before starting the container:

- **`from_env` sources**: The host environment variable must be set and non-empty. Unset or empty host environment variables cause a startup validation error.
- **`from_file` sources**: The host file path must exist and be a regular file.
- **World-readable permission requirement (`o+r`)**: Applies **only** when both destination is `file` (`secrets.file.<NAME>`) and source is `from_file` (combination 4). In combination 4, the host file is bind-mounted directly to unprivileged UID 1000, requiring world-readable permissions (`o+r`). For combination 2 (`secrets.env.<NAME>` with `from_file`), the file is read by the root entrypoint during startup before dropping privileges, so world-readable permissions are not required.

#### Intra-Scope Constraints
Within a single scope (`[defaults]` or a specific workspace `[workspaces.<ws>]`), a secret `<NAME>` cannot be declared in both `secrets.env` and `secrets.file`.

### Merge semantics

Environment variables from multiple sources are merged in this order (later entries win for the same key):

1. `[defaults].env` — global inline values
2. `[defaults].env_file` — global file values (in list order)
3. Workspace `env_file` — per-workspace file values (in list order)
4. Workspace `env` — per-workspace inline values (highest priority)

`allowed_hosts` and `allowed_networks` use union deduplication: the global list is merged with the workspace list, with duplicates removed, preserving order.

`mounts` follows a three-layer merge keyed by container path. Default mounts (built into jailoc) are applied first, then `[defaults].mounts`, then per-workspace `mounts`. For each container path, the last layer to set it wins. An entry with an empty host source removes the mount for that container path. The built-in default mounts are:

| Container path | Host source | Mode |
|---|---|---|
| `/home/agent/.config/opencode` | `~/.config/opencode` | `rw` |
| `/home/agent/.opencode` | `~/.opencode` | `rw` |
| `/home/agent/.claude/transcripts` | `~/.claude/transcripts` | `rw` |
| `/home/agent/.agents` | `~/.agents` | `ro` |

OpenCode configuration directories are mounted read-write because the agent needs write access to persist settings changes, install tools and MCPs, and update its own configuration at runtime.

`ssh_auth_sock`, `git_config`, `expose_port`, `enable_docker`, `filtered_dns`, and `ca_bundle` inherit from `[defaults]` when not set in the workspace. When set explicitly in a workspace, the workspace value takes precedence. `git_config`, `expose_port`, and `enable_docker` fall back to `true` when neither the workspace nor defaults set it; `filtered_dns` falls back to `false` and `ca_bundle` falls back to automatic discovery. An empty workspace `dns_upstream` inherits the default upstream; an omitted `dns_blocked_zones` inherits the default list. These lists replace defaults when specified, rather than merging.

`cpu` and `memory` inherit from `[defaults]` when not set in the workspace. When set explicitly in a workspace, the workspace value takes precedence. `cpu` falls back to `2.0` and `memory` falls back to `"4g"` when neither the workspace nor defaults set them.

`secrets` entries merge by secret name across layers. Global default secrets under `[defaults.secrets.env.<NAME>]` or `[defaults.secrets.file.<NAME>]` apply to all workspaces. If a workspace declares a secret with the same `<NAME>` under `[workspaces.<ws>.secrets.env.<NAME>]` or `[workspaces.<ws>.secrets.file.<NAME>]`, the workspace secret entry replaces the defaults secret entry entirely, including across secret kinds (for example, a workspace environment secret replaces a default file secret of the same name). There is no syntax to unset an inherited default secret in a workspace.

---

## Port Allocation

Each workspace is assigned a fixed host port based on the alphabetical sort order of all configured workspace names:

```
port = 4096 + index
```

Where `index` is the zero-based position of the workspace name when all workspace names are sorted alphabetically.

| Example workspaces (sorted) | Assigned port |
|-----------------------------|---------------|
| `alpha` (index 0) | 4096 |
| `beta` (index 1) | 4097 |
| `gamma` (index 2) | 4098 |

Port assignments shift when workspace names are added or removed. Run `jailoc status` to see current assignments.

When `expose_port` is `false` for a workspace, the port is still allocated but not exposed to the host. The workspace remains accessible via `jailoc --exec`, but `jailoc --remote` does not work because readiness detection and remote attach require a published localhost port.

---

See the [workspace configuration how-to](../how-to/workspace-configuration.md) for step-by-step setup instructions.
