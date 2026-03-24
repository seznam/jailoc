# Configuration Reference

Configuration file location: `~/.config/jailoc/config.toml`

The file is auto-created with defaults on first run. All fields are optional unless noted.

---

## `[image]`

Global image settings applied to all workspaces unless overridden at the workspace level.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `repository` | string | `ghcr.io/seznam/jailoc` | Base image registry URL used for registry pull when no `dockerfile` is set. |
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for the base image. When set, takes priority over `repository` and the embedded fallback. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. Supports `~` expansion for local paths. |

### Example

```toml
[image]
repository = "registry.example.com/myorg/jailoc"
dockerfile = "https://example.com/Dockerfiles/custom"
```

Or with a local Dockerfile:

```toml
[image]
dockerfile = "/opt/myorg/base.Dockerfile"
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
| `build_context` | string | (none) | Docker build context directory for the workspace overlay build. When empty and `dockerfile` is a local path, defaults to the parent directory of the Dockerfile. When empty and `dockerfile` is an HTTP URL, a temporary directory is used. Supports `~` expansion. |
| `mode` | string | `""` | Connection mode for `jailoc attach` and the root `jailoc` command. Accepted values: `"remote"`, `"exec"`, `""` (auto-detect). |
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for a workspace-specific overlay image. Builds on top of the base image resolved by `[image]` settings. Build failure is fatal. Maximum file size for HTTP sources: 1 MiB. Supports `~` expansion for local paths. |

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

### `dockerfile` fields

Accepted values:

- **Absolute local paths**: must start with `/` (e.g. `/opt/my.Dockerfile`)
- **Tilde paths**: must start with `~` (e.g. `~/my.Dockerfile`); `~` is expanded to `$HOME` before use
- **HTTP(S) URLs**: must have an `http` or `https` scheme and a non-empty host component. Paths like `http:///path` are rejected.

Relative paths and other URL schemes (e.g. `ftp://`, `file://`) are not accepted.

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
