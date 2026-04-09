# Configuration Reference

Configuration file location: `~/.config/jailoc/config.toml`

The file is auto-created with defaults on first run. All fields are optional unless noted.

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
| `allowed_hosts` | string[] | `[]` | Hostnames allowed through the firewall for all workspaces. Merged with per-workspace `allowed_hosts`. |
| `allowed_networks` | string[] | `[]` | CIDR ranges allowed through the firewall for all workspaces. Merged with per-workspace `allowed_networks`. |
| `ssh_auth_sock` | bool | `false` | Mount the host SSH agent socket into the container. Auto-detects the socket: Docker Desktop/OrbStack magic path first, then `$SSH_AUTH_SOCK`. Also mounts `~/.ssh/known_hosts` read-only when enabled. |
| `git_config` | bool | `true` | Mount the host Git configuration (`~/.gitconfig` or `~/.config/git/config`) read-only into the container. |

### Example

```toml
[defaults]
image = "myregistry.example.com/myteam/opencode-base:v1.2.3"
env = ["GOPRIVATE=*.example.com", "NPM_REGISTRY=https://npm.example.com"]
env_file = ["~/.config/jailoc/shared.env"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8"]
ssh_auth_sock = true
git_config = true
```

---

## `[workspaces.<name>]`

Each workspace is declared as a TOML table under `[workspaces]`, keyed by name.

**Workspace name constraints:** must match `^[a-z0-9-]+$` (lowercase letters, digits, and hyphens only).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `paths` | string[] | (required) | Directories bind-mounted into the container at their original absolute paths. The first path becomes the container's working directory. Supports `~` expansion. |
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

!!! note
    `image` is mutually exclusive with `dockerfile` and `build_context`. Setting `image` alongside either of those fields is a validation error.

### Example

```toml
[workspaces.my-project]
paths = ["~/projects/my-project", "~/shared/libs"]
allowed_hosts = ["api.example.com", "pypi.org"]
allowed_networks = ["10.0.0.0/8"]
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

### `allowed_networks`

Each entry must be a valid CIDR notation string as accepted by Go's `net.ParseCIDR`. Invalid CIDR values are rejected at config load time.

### Workspace `image`

The workspace `image` field is mutually exclusive with `dockerfile` and `build_context`. The following combinations are rejected at config load time:

- `image` set together with `dockerfile`
- `image` set together with `build_context`

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
| `OPENCODE_SERVER_PASSWORD` | opencode server auth |
| `DOCKER_HOST` | DinD TLS connection |
| `DOCKER_TLS_CERTDIR` | DinD TLS certs |
| `DOCKER_CERT_PATH` | DinD TLS certs |
| `DOCKER_TLS_VERIFY` | DinD TLS verification |
| `SSH_AUTH_SOCK` | SSH agent passthrough |

### `env_file`

Each path must be absolute (starting with `/`) or start with `~` (expanded to `$HOME`). Files must exist at config load time. The file format is Docker `.env` compatible:

- Lines in `KEY=VALUE` format
- Lines starting with `#` are comments
- Empty lines are ignored
- Values may be quoted (`KEY="value with spaces"`)
- Inline comments after values are stripped

Values are treated as literal strings — no host environment variable expansion is performed.

### Merge semantics

Environment variables from multiple sources are merged in this order (later entries win for the same key):

1. `[defaults].env` — global inline values
2. `[defaults].env_file` — global file values (in list order)
3. Workspace `env_file` — per-workspace file values (in list order)
4. Workspace `env` — per-workspace inline values (highest priority)

`allowed_hosts` and `allowed_networks` use union deduplication: the global list is merged with the workspace list, with duplicates removed, preserving order.

`ssh_auth_sock` and `git_config` inherit from `[defaults]` when not set in the workspace. When set explicitly in a workspace, the workspace value takes precedence. `git_config` falls back to `true` when neither the workspace nor defaults set it.

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

---

See the [workspace configuration how-to](../how-to/workspace-configuration.md) for step-by-step setup instructions.
