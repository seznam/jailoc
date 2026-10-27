# CLI Reference

## Synopsis

```
jailoc [path] [flags]
jailoc [command] [flags]
```

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--workspace` | `-w` | `default` | Target workspace name. Overrides path-based auto-detection where applicable. |
| `--no-color` | | `false` | Disable colored output. |

### Workspace resolution

Commands that accept an optional `[workspace]` argument use it first. Without the argument and without `--workspace`, they resolve the workspace whose configured path best matches the current working directory (longest prefix) and fall back to `default`. See the [workspace configuration how-to](../how-to/workspace-configuration.md) for the full resolution order.

---

## Commands

### `jailoc`

Open an OpenCode session in the workspace VM.

```
jailoc [path] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--new` | | Start a new agent session instead of attaching to the one running in the target directory. |
| `--attach ID` | | Attach to the agent session with this ID (see [`jailoc sessions`](#jailoc-sessions)). |
| `--continue` | `-c` | Start a session that continues the last OpenCode session. |
| `--session ID` | `-s` | Start a session that continues the OpenCode session with this ID. |
| `--version` | `-v` | Print the jailoc version. |

Mutually exclusive: `--session` with `--continue`, `--new` with `--attach`, `--attach` with `--session` or `--continue`.

The target directory is `path` (`~` is expanded) or the current working directory, and must be a directory. Without `--workspace`, the workspace is resolved from the target directory. When the directory is not under any path of the workspace, jailoc asks whether to add it.

If the workspace VM is not running, jailoc starts it as [`jailoc up`](#jailoc-up) does. It then runs `opencode` inside the VM over SSH, in a detachable session whose working directory is the target directory:

- Without flags, attaches to the session already running in the target directory, or starts one.
- `--session` and `--continue` always start a new session.
- `Ctrl-\` or closing the terminal detaches; the agent keeps running.
- Attaching from a second terminal takes the session over and detaches the first one.

See the [sessions how-to](../how-to/sessions.md).

---

### `jailoc up`

Start a workspace VM.

```
jailoc up [workspace] [flags]
```

Validates secret sources on the host, builds the workspace image with the host Docker daemon or in a builder VM (see [Image Resolution](image-resolution.md)), unpacks it into the VM image cache, boots it as the root file system of the workspace VM behind the host-side network broker, and delivers `env` and secrets to the guest.

When the VM already runs, `jailoc up` does what [`jailoc reload`](#jailoc-reload) does.

---

### `jailoc down`

Stop a workspace VM.

```
jailoc down [workspace] [flags]
```

Shuts down the workspace VM and its network broker. All agent sessions end. The persistent home directories (`~/.local/share/opencode`, `~/.local/state/opencode` and `~/.cache` of the agent) are kept.

| Flag | Description |
|------|-------------|
| `--purge` | Also delete the VM state, the persistent home directories, the Docker data disk of the workspace VM and, with `isolated_build`, the workspace's builder VM and build cache. |

---

### `jailoc restart`

Stop and start a workspace VM with the current configuration.

```
jailoc restart [workspace] [flags]
```

Equivalent to `jailoc down` followed by `jailoc up`: rebuilds the workspace image and ends running sessions. If the workspace is not running, starts it.

---

### `jailoc reload`

Apply configuration changes to a running workspace VM without restarting it.

```
jailoc reload [workspace] [flags]
```

Updated live:

- Network policy: `allowed_hosts`, `allowed_networks`, `filtered_dns`
- Published `ports`
- SSH agent: `ssh_auth_sock`, with the socket read again from `$SSH_AUTH_SOCK`
- `ca_bundle`
- `snapshots`, including the Git configuration and `known_hosts`: every reload copies the sources again, adds new entries and removes dropped ones
- `env`, `env_file` and secrets

Running sessions keep running with their old environment; sessions started afterwards get the updated `env`, `env_file` entries and env secrets. Files opened before a reload keep their old snapshot content.

Changes to the following need a new VM. `jailoc reload` lists them, and [`jailoc restart`](#jailoc-restart) applies them:

- `paths` and `mounts`
- `cpu`, `memory`
- `enable_docker`, `protect_git`
- the working directory (first entry of `paths`)
- the jailoc guest binary, after a jailoc upgrade

Fails when the workspace is not running.

---

### `jailoc status`

Show whether the workspace VM is running.

```
jailoc status [workspace] [flags]
```

---

### `jailoc logs`

Print the VM supervisor log and the guest console log of a workspace.

```
jailoc logs [workspace] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Follow log output until interrupted. |

---

### `jailoc sessions`

List the agent sessions running in a workspace VM, or stop one.

```
jailoc sessions [flags]
jailoc sessions stop ID [flags]
```

Prints each session's ID, state (`attached` or `detached`), start time, directory and command, most recently attached first. `stop` ends the session's agent; an attached terminal is disconnected. Sessions end when the workspace VM stops.

---

### `jailoc config`

Print the resolved configuration.

```
jailoc config [flags]
```

Reads `~/.config/jailoc/config.toml`, resolves defaults and `~` expansions, and prints the result. Secrets are listed per scope, under `Defaults Secrets:` and under each workspace, as `NAME (<destination> from_<source> <reference>)`, for example `GH_TOKEN (env from_env HOST_GITHUB_TOKEN)`, `NPM_TOKEN (env from_command "op read op://dev/npm/token")` or `db_cert (file from_file /home/you/.config/db.pem)`. Only the source reference is printed; secret values are never read, commands are never run.

---

### `jailoc add`

Add a directory to a workspace's `paths` list.

```
jailoc add [path] [flags]
```

Appends `path` (default: the current working directory) to `workspaces.<name>.paths` in `~/.config/jailoc/config.toml`. The path must not be under a forbidden system prefix; see the [configuration reference](configuration.md#paths). Secret sources of the target workspace are validated before the configuration changes. If the workspace VM is running, it is restarted so the new share takes effect.

Without `--workspace`, the workspace is resolved from the path being added (longest prefix). If `--workspace` is set and the path is not under any configured path of that workspace, `jailoc add` returns an error.

---

### `jailoc migrate`

Rewrite a jailoc v1 `config.toml` for the VM backend.

```
jailoc migrate [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--dry-run` | | Print the planned changes and the resulting `config.toml` without changing anything. |
| `--yes` | `-y` | Accept the default answer to every question. Required when standard input is not a terminal. |

A workspace counts as a jailoc v1 Docker workspace unless its effective `backend` is `"vm"`. `jailoc migrate` rewrites `~/.config/jailoc/config.toml`:

- Sets `backend = "vm"` in `[defaults]` and removes every workspace-level `backend`.
- Lists the jailoc v1 settings `mode`, `password_mode` and `expose_port` per scope and removes them after confirmation. Declining stops the migration.
- Asks for every `mounts` entry of `[defaults]` and of each Docker workspace whether it stays a directory share (default for directories), becomes a read-only [snapshot](configuration.md#snapshots) (default for files), or is dropped. Files cannot stay mounts.

For Docker workspaces it then stops running ones after confirmation and copies the Docker volumes `opencode-data-<workspace>` and `opencode-cache-<workspace>` into the VM's persistent home directories, unless a mount covers those paths or VM data already exists there. The volumes are left in place. Without a reachable Docker daemon, these steps are skipped after confirmation.

The previous configuration is saved as `config.toml.pre-vm-migration` next to it. Comments are not carried over to the rewritten file. See [Migrate from jailoc v1](../how-to/migrate-from-v1.md).

---

### `jailoc completion`

Generate a shell completion script.

```
jailoc completion bash|fish|powershell|zsh
```

---

## Update Checks

jailoc checks GitHub Releases for newer versions on every command run. The check runs asynchronously in the background and does not block command execution.

- **Frequency**: cached for 24 hours.
- **Output**: when a newer version is available, a notice is printed to stderr. If stderr is not a TTY, the notice is suppressed, but the check still updates the cache.
- **Color**: respects `--no-color`.

Update checks are skipped when:

- the build version is `dev` or `(devel)` (local `go build` without release ldflags),
- the `CI` environment variable is set to any non-empty value,
- the `JAILOC_NO_UPDATE_NOTIFIER` environment variable is set to any non-empty value.

| Variable | Default | Description |
|----------|---------|-------------|
| `JAILOC_NO_UPDATE_NOTIFIER` | (unset) | Set to any non-empty value to disable update checks. |
