# CLI Reference

## Synopsis

```
jailoc [command] [flags]
```

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--workspace` | `-w` | `default` | Target workspace name. Overrides CWD-based auto-detection where applicable. |

---

## Commands

### `jailoc`

Auto-detect the workspace from the current working directory. If no matching workspace is found, prompts to add the current directory. If the workspace is not running, starts it. Attaches to the running environment.

```
jailoc [flags]
```

| Flag | Description |
|------|-------------|
| `--remote` | Force remote connection mode (runs `opencode attach` on the host). |
| `--exec` | Force exec connection mode (runs `docker exec` into the container). |
| `--dir` | Directory to open in opencode (forwarded as `--dir` to `opencode attach`). |
| `--new` | Start a new agent session instead of attaching to the one running in the current directory. Backend `"vm"` only. |
| `--attach ID` | Attach to the agent session with this ID (see [`jailoc sessions`](#jailoc-sessions)). Backend `"vm"` only; cannot be combined with `--new`, `--session` or `--continue`. |

When run from a subdirectory of a workspace path, jailoc resolves the current working directory and passes it as `--dir` to `opencode attach`, so the agent opens that directory instead of the workspace root.

For workspaces with `backend = "vm"`, runs `opencode` inside the workspace VM in a detachable session; `--remote` is rejected and `mode` is ignored. Without flags, jailoc attaches to the session already running in the current directory, or starts one. `--session` and `--continue` always start a new session. Press `Ctrl-\` or close the terminal to detach; the agent keeps running. Attaching from a second terminal takes the session over and detaches the first one.

When neither `--remote` nor `--exec` is specified, the connection mode is determined by the workspace `mode` field in configuration, falling back to auto-detection (checks for `opencode`, then `opencode-cli` on PATH). See the [access modes how-to](../how-to/access-modes.md) for configuration steps, or the [access modes explanation](../explanation/access-modes.md) for the difference between modes.

---

### `jailoc up`

Start the Docker Compose environment for the target workspace. No-op if the workspace containers are already running.

```
jailoc up [flags]
```

Resolves the container image (see [Image Resolution](image-resolution.md)), validates secret sources (verifying host environment variables exist and are non-empty, and secret files exist with valid permissions), generates a `docker-compose.yml` in `~/.cache/jailoc/{workspace}/`, and starts two containers: `opencode` and `dind`.

For workspaces with `backend = "vm"`, builds the workspace image with the host Docker daemon or in a builder VM (see `[vm] builder`), then boots the image as the root file system of the workspace VM and delivers secrets to it. No containers or compose file are involved; see [Run a workspace in a VM](../how-to/vm-backend.md).

When `--workspace` is not set, resolves the workspace whose configured path best matches the current working directory (longest prefix). Falls back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc down`

Stop and remove the containers for the target workspace.

```
jailoc down [flags]
```

Equivalent to `docker compose down` on the generated compose file. Does not remove named volumes or the image.

For workspaces with `backend = "vm"`, shuts down the workspace VM and its network broker instead. The persistent home directories (`~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache` of the agent) are kept.

| Flag | Description |
|------|-------------|
| `--purge` | Also delete the VM state, the persistent home directories, the Docker data disk of the workspace VM and, with `isolated_build`, the workspace's builder VM and build cache. Applies to `backend = "vm"` only. |

When `--workspace` is not set, resolves the workspace whose configured path best matches the current working directory (longest prefix). Falls back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc restart`

Stop and restart the Docker Compose environment for a workspace.

```
jailoc restart [flags]
```

Equivalent to `jailoc down` followed by `jailoc up`. Regenerates the compose configuration from the current `config.toml` before bringing the workspace back up. If the workspace is not running, starts it without error.

When `--workspace` is not set, resolves the workspace whose configured path best matches the current working directory (longest prefix). Falls back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc status`

Print the state and assigned port for each configured workspace.

```
jailoc status [flags]
```

Output shows the container state (running, stopped, or unknown) and the assigned host port for the resolved workspace. Workspaces with `expose_port = false` show "not exposed (exec-only)" instead of a port number.

For workspaces with `backend = "vm"`, shows the backend and whether the workspace VM is running.

When `--workspace` is not set, resolves the workspace whose configured path best matches the current working directory (longest prefix). Falls back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc logs`

Stream container logs from the target workspace environment.

```
jailoc logs [flags]
```

Streams combined stdout and stderr from the `opencode` and `dind` containers. Follows log output until interrupted.

For workspaces with `backend = "vm"`, prints the VM supervisor log and the guest console log instead.

When no positional workspace argument and `--workspace` is not set, resolves the workspace from the current working directory (longest prefix). Falls back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc config`

Print the current resolved configuration.

```
jailoc config [flags]
```

Reads `~/.config/jailoc/config.toml`, resolves all defaults and `~` expansions, and prints the result. Useful for verifying what values are in effect. Secrets are listed per scope — under `Defaults Secrets:` and under each workspace — as `NAME (<destination> from_<source> <reference>)`, for example `GH_TOKEN (env from_env HOST_GITHUB_TOKEN)` or `db_cert (file from_file /home/you/.config/db.pem)`. Only the source reference is printed; secret values are never resolved or displayed.

---

### `jailoc add`

Add the current working directory to the target workspace's `paths` list.

```
jailoc add [flags]
```

Appends the current directory to `workspaces.<name>.paths` in `~/.config/jailoc/config.toml`. The path must not be under a forbidden system prefix. See the [configuration reference](configuration.md) for path validation rules. Validates secret sources for the target workspace before updating configuration. If the workspace is running, stops and recreates its Compose containers so updated CA trust and mounts take effect; named volumes are preserved.

When `--workspace` is not set, resolves the workspace from the path being added (longest prefix). If `--workspace` is set explicitly and the path is not under any of that workspace's configured paths, `jailoc add` returns an error. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

### `jailoc sessions`

List the agent sessions running in a VM workspace, or stop one.

```
jailoc sessions [flags]
jailoc sessions stop ID [flags]
```

Prints each session's ID, state (`attached` or `detached`), start time, directory and command, most recently attached first. `stop` ends the session's agent; an attached terminal is disconnected. Sessions end when the workspace VM stops. Backend `"vm"` only.

When `--workspace` is not set, resolves the workspace from the current working directory (longest prefix). Falls back to `default`.

---

### `jailoc migrate`

Move every workspace to the VM backend.

```
jailoc migrate [flags]
```

| Flag | Description |
|------|-------------|
| `--dry-run` | Print the planned changes and the resulting `config.toml` without changing anything. |
| `-y`, `--yes` | Accept the default answer to every question. Required when standard input is not a terminal. |

Rewrites `~/.config/jailoc/config.toml`:

- Sets `backend = "vm"` in `[defaults]` and removes every workspace-level `backend`.
- Lists settings the VM backend rejects or ignores (`mode = "remote"`, `expose_port = true`) per scope and removes them after confirmation. Declining stops the migration.
- Asks for every `mounts` entry of `[defaults]` and of each Docker workspace whether it stays a mount (default for directories), becomes a read-only [copy](configuration.md#copies) (default for files), or is dropped. Files cannot stay mounts.

For Docker workspaces it then stops running ones after confirmation and copies the Docker volumes `opencode-data-<workspace>` and `opencode-cache-<workspace>` into the VM's persistent home directories, unless a mount covers those paths or VM data already exists there. The volumes are left in place. Without a reachable Docker daemon, these steps are skipped after confirmation.

The previous configuration is saved as `config.toml.pre-vm-migration` next to it. Comments are not carried over to the rewritten file.

---

## Update Checks

jailoc checks GitHub Releases for newer versions on every command run. The check runs asynchronously in the background and does not block command execution.

### Behavior

- **Frequency**: Cached for 24 hours. Subsequent checks within the cache window return the cached result.
- **Output**: When a newer version is available, a notice is printed to stderr. If stderr is not a TTY (e.g., when output is redirected or piped), the notice is suppressed, but the background check still runs and updates the cache.
- **Non-blocking**: The check runs in a goroutine and results are collected at command completion. Command execution is not delayed.
- **Color**: Respects the `--no-color` flag. When `--no-color` is set and a notice is printed, it appears without ANSI color codes.

### Disabling Update Checks

Update checks are skipped in the following conditions:

- **Development builds**: Builds whose version is `dev` or `(devel)` (e.g., local `go build` without release ldflags)
- **CI environments**: When the `CI` environment variable is set to any non-empty value
- **Explicit opt-out**: When the `JAILOC_NO_UPDATE_NOTIFIER` environment variable is set to any non-empty value

To disable update checks, set the environment variable:

```bash
export JAILOC_NO_UPDATE_NOTIFIER=1
jailoc up
```

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `JAILOC_NO_UPDATE_NOTIFIER` | (unset) | Set to any non-empty value to disable update checks. |
