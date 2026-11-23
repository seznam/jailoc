# Admin Policy Reference

A root-owned `policy.toml` file pins security constraints that user configuration (`~/.config/jailoc/config.toml`, `--config`, or `JAILOC_CONFIG`) cannot loosen or bypass.

## File locations and trust checks

| Platform | Search order |
|----------|--------------|
| Linux | `/etc/jailoc/policy.toml` |
| macOS | `/Library/Application Support/jailoc/policy.toml`, then `/etc/jailoc/policy.toml` |

- jailoc loads the first existing file in the platform search order. When no policy file exists, no admin policy is active.
- `--config` and `JAILOC_CONFIG` do not affect policy discovery.
- When a policy file exists, jailoc fails closed unless the file and every ancestor directory up to `/` (including across symlinks) are owned by `root` (`uid 0`) and neither group-writable nor world-writable (`mode & 0o022 == 0`), and the resolved target is a regular file.
- Unknown keys in `policy.toml` are rejected.

## Enforcement

- **Strict reject**: Any user configuration or resolved workspace that violates a constraint is rejected with an error naming the policy file and every violation.
- **Live reload (`jailoc reload`)**: Rejects a reload when the running VM violates a restart-only policy constraint (`enable_docker`, `protect_git`, `network.ipv6`, or read-write shares under `forbid_rw_mounts`), instructing the user to run `jailoc restart`.
- **Network deny floor**: `[network] deny` both rejects overlapping user `allow` rules statically and injects a top-priority `policy` deny rule into the workspace and builder network brokers.
- **Inspection (`jailoc config`)**: Prints the active policy path, active constraints, and any workspace violations.

!!! note
    An admin policy binds only the installed `jailoc` binary. A user who builds or runs an unpatched binary is outside its scope.

---

## `[vm]`

| Field | Type | Description |
|-------|------|-------------|
| `require_builder` | string | Set to `"vm"` to require `[vm] builder = "vm"`, forbidding host Docker builds (`"docker"` and `"auto"`). |
| `require_isolated_build` | bool | Require `isolated_build = true` on every workspace. |
| `forbid_custom_runner` | bool | Reject `[vm] runner` and `[vm] lib_dir` overrides. |

---

## `[workspace]`

| Field | Type | Description |
|-------|------|-------------|
| `require_protect_git` | bool | Require `protect_git = true` on every workspace. |
| `forbid_docker` | bool | Reject `enable_docker = true`. |
| `forbid_ssh_auth_sock` | bool | Reject `ssh_auth_sock = true`. |
| `forbid_gpg_agent` | bool | Reject `gpg_agent = true`. |
| `forbid_ports` | bool | Reject non-empty `ports`. |
| `forbid_host_ports` | bool | Reject non-empty `host_ports`. |
| `forbid_git_config` | bool | Reject `git_config = true`. |
| `forbid_import_agent_config` | bool | Reject `import_agent_config = true`. |
| `forbid_custom_dockerfile` | bool | Reject `[base] dockerfile`, workspace `dockerfile`, and workspace `image` overrides. |
| `allowed_agents` | list of strings | Built-in agent harnesses permitted (`opencode`, `opencode1`, `opencode2`, `claude`, `copilot`, `omo`, `agy`). Custom agents derived via `from` are checked against their base harness. Omitted means unrestricted; `[]` permits no agent. |

---

## `[filesystem]`

All path prefixes expand `~` to the user's home directory and resolve symlinks (including across parent directories of non-existent leaf paths) before prefix matching.

| Field | Type | Description |
|-------|------|-------------|
| `allowed_paths` | list of strings | Prefix allowlist for workspace `paths`. Omitted means unrestricted; `[]` permits no paths. |
| `allowed_mount_sources` | list of strings | Prefix allowlist for all resolved `mounts` host paths, including built-in agent default mounts. Omitted means unrestricted; `[]` permits no mounts. |
| `allowed_snapshot_sources` | list of strings | Prefix allowlist for `snapshots` host paths. Omitted means unrestricted; `[]` permits no snapshots. |
| `forbidden_mount_sources` | list of strings | Additional host path prefixes forbidden in `paths`, `mounts`, and `snapshots`. |
| `forbid_rw_mounts` | bool | Reject read-write `mounts` (`:rw`) and non-empty workspace `paths`. |

---

## `[network]`

| Field | Type | Description |
|-------|------|-------------|
| `require_rules` | bool | Reject legacy `allowed_hosts` and `allowed_networks`; workspaces must use `[[*.network.rules]]`. |
| `allowed_targets` | list of strings | Ceiling allowlist of hosts, wildcards (`*.example.com`), or CIDRs permitted in non-agent `allow` rules. Omitted means unrestricted; `[]` forbids all non-agent `allow` rules. |
| `allowed_agent_endpoints` | list of strings | Ceiling allowlist of host names or `*.domain` wildcards permitted in the workspace's `agent` rule (`agent_endpoints` and single-provider built-in endpoints). Omitted means unrestricted; `[]` permits no agent endpoints. |
| `allowed_internal_zones` | list of strings | Ceiling allowlist of DNS zones permitted in `network.internal_zones`. Omitted means unrestricted; `[]` permits no internal zones. |
| `forbid_ipv6` | bool | Reject `network.ipv6 = true`. |
| `forbid_wildcard_allow` | bool | Reject `*`, `0.0.0.0/0`, and `::/0` in `allow` rules. |
| `forbid_private_networks` | bool | Reject private, loopback, link-local, CGNAT, and ULA CIDRs in `allow` rules. |
| `deny` | list of strings | Targets (hosts, wildcards, or CIDRs) rejected statically when any user `allow` rule overlaps them, and enforced dynamically as a `policy` deny rule in the network broker. |
| `require_dns` | string | Require `network.dns` to equal this routable IPv4 address. |

---

## `[secrets]`

| Field | Type | Description |
|-------|------|-------------|
| `forbid_from_command` | bool | Reject secrets that use `from_command`. |
| `allowed_secret_files` | list of strings | Prefix allowlist (with symlink resolution) for `from_file` secret paths. Omitted means unrestricted; `[]` permits no `from_file` secrets. |

---

## Example

```toml
[vm]
require_builder = "vm"
forbid_custom_runner = true

[workspace]
require_protect_git = true
forbid_docker = true
forbid_ssh_auth_sock = true
forbid_gpg_agent = true
forbid_ports = true
forbid_host_ports = true
allowed_agents = ["claude", "opencode"]

[filesystem]
allowed_paths = ["~/work"]
forbidden_mount_sources = ["~/private"]

[network]
require_rules = true
allowed_agent_endpoints = ["api.anthropic.com", "api.githubcopilot.com"]
forbid_wildcard_allow = true
forbid_private_networks = true
deny = ["pastebin.com", "*.ngrok.io"]

[secrets]
forbid_from_command = true
```
