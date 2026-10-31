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

Commands pick the workspace in this order:

1. The `[workspace]` argument, for commands that accept one.
2. The `--workspace` flag.
3. The workspace whose `paths` entry is the longest prefix of the current directory. If several workspaces tie on the deepest match, the command fails and asks for `-w`.
4. `default`.

With `--all` (`-A`), `up`, `down`, `restart`, `reload` and `status` act on every configured workspace one after another, in name order, and do not take a `[workspace]` argument or `--workspace`. A failure in one workspace does not stop the others; the command reports all failures and exits non-zero.

`jailoc add` matches the path being added instead of the current directory. With `--workspace`, the added path must lie under one of that workspace's paths.

---

## Commands

### `jailoc`

Open a session of the workspace's coding agent in the workspace VM.

```
jailoc [path] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--new` | | Start a new agent session instead of attaching to the one running in the target directory. |
| `--attach ID` | | Attach to the agent session with this ID (see [`jailoc sessions`](#jailoc-sessions)). |
| `--continue` | `-c` | Start a session that continues the agent's last conversation. |
| `--session ID` | `-s` | Start a session that continues the agent conversation with this ID. |
| `--detach-keys KEYS` | | Key sequence that detaches from the session, for example `ctrl-a,d`. Overrides [`detach_keys`](configuration.md#top-level-fields). Default `ctrl-b,d`. |
| `--version` | `-v` | Print the jailoc version. |

Mutually exclusive: `--session` with `--continue`, `--new` with `--attach`, `--attach` with `--session` or `--continue`.

The target directory is `path` (`~` is expanded) or the current working directory, and must be a directory. Without `--workspace`, the workspace is resolved from the target directory. When the directory is not under any path of the workspace, jailoc asks whether to add it. When several workspaces match the directory at the same depth, jailoc fails and asks for `--workspace`.

If the workspace VM is not running, jailoc starts it as [`jailoc up`](#jailoc-up) does. It checks that the agent binary is on `PATH` in the VM and runs the agent (see [Coding agents](configuration.md#coding-agents)) over SSH, in a detachable session whose working directory is the target directory:

- Without flags, attaches to the session already running in the target directory, or starts one.
- `--session` and `--continue` always start a new session.
- The detach keys (`Ctrl-b`, then `d`, by default) or closing the terminal detach; the agent keeps running. Typing `Ctrl-b` twice sends one `Ctrl-b` to the agent.
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

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Start every configured workspace, in name order. |

---

### `jailoc down`

Stop a workspace VM.

```
jailoc down [workspace] [flags]
```

Shuts down the workspace VM and its network broker. All agent sessions end. The persistent home directories (the [agent's state directories](configuration.md#coding-agents) and `~/.cache`) are kept.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Stop every configured workspace. Combined with `--purge`, purges every workspace. |
| `--purge` | | Also delete the VM state, the persistent home directories, the Docker data disk of the workspace VM and, with `isolated_build`, the workspace's builder VM and build cache. Named agent [profiles](configuration.md#agent-profiles) are kept. |

---

### `jailoc restart`

Stop and start a workspace VM with the current configuration.

```
jailoc restart [workspace] [flags]
```

Equivalent to `jailoc down` followed by `jailoc up`: rebuilds the workspace image and ends running sessions. If the workspace is not running, starts it.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Restart every running workspace. Stopped workspaces stay stopped. |

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

- `paths` and `mounts`, including `agents.<name>.mounts`
- the agent's `profile`
- `cpu`, `memory`
- `enable_docker`, `protect_git`
- the working directory (first entry of `paths`, or `/home/agent` when `paths` is empty)
- the jailoc guest binary, after a jailoc upgrade

Fails when the workspace is not running.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Reload every running workspace. Stopped workspaces are skipped. |

---

### `jailoc status`

Show the agent of the workspace (with the package version jailoc installs and extra `args`) and whether its VM is running.

For a running VM it also shows the uptime, CPUs, memory and Docker disk, where the agent's persistent home directories live (the named [profile](configuration.md#agent-profiles), `workspace`, or `none` for `profile = false`), the number of agent sessions, and whether the VM runs the current configuration: `jailoc restart` is needed when a setting that needs a new VM changed (`cpu`, `memory`, `agent`, `profile`, `enable_docker`, `protect_git`, or an upgraded jailoc), `jailoc reload` when the configuration file changed after the VM started or was last reloaded. For a stopped VM it shows when it stopped and the last error. `Data` is the VM state directory on the host.

```
jailoc status [workspace] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Show the status of every configured workspace. |

```
Workspace: myproject
Backend:   vm
Agent:     claude (@anthropic-ai/claude-code@2.1.312)
State:     running (supervisor pid 4242)
Started:   2026-10-31T09:12:44+01:00 (up 2h5m)
Resources: 2 CPUs, 4096 MiB memory
Profile:   workspace
Sessions:  2 (1 attached); `jailoc sessions myproject` lists them
Config:    changed since the VM started; `jailoc reload myproject` applies it
Data:      ~/.cache/jailoc-vm/workspaces/myproject
```

---

### `jailoc list`

List every configured workspace with its agent, whether its VM is running, the agent sessions of a running VM (and how many have a terminal attached) and its paths. `*` marks the workspace of the current directory. Alias: `jailoc ls`.

```
jailoc list
```

```
  WORKSPACE  AGENT      STATE    SESSIONS        PATHS
  default    opencode   running  0               ~/projects
* myproject  claude     running  2 (1 attached)  ~/projects/myproject
  scratch    opencode1  stopped  -               -
```

`SESSIONS` shows `?` when jailoc cannot reach a running VM within 5 seconds.

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

### `jailoc traffic`

Print the network activity of a workspace VM from the network broker's log: allowed and denied connections with the DNS name they were looked up by, the TLS server name and ALPN or the HTTP method, host and path (without query string) of allowed TCP connections, DNS answers, names blocked by `filtered_dns`, failed dials and connections to published ports. The log persists after the VM stops.

```
jailoc traffic [workspace] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Keep printing new events until interrupted. |
| `--lines N` | `-n` | Number of past events to print. Default `100`; `0` prints none, `-1` all. |
| `--json` | | Print the shown events as their JSON log lines, unchanged. |
| `--all` | | Also show DHCP, NIC, gateway service and jailoc's own SSH connections. |

Without `--json`, control characters in logged values are escaped. TLS connections are identified from the unencrypted ClientHello only; their content is not decrypted.

---

### `jailoc strace`

Print the processes started in a workspace VM: time, PID, parent PID, user, working directory and command line. The guest records every process start from boot; the log of the previous boot is kept as `exec.prev.jsonl` in the VM state directory.

```
jailoc strace [workspace] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Keep printing new processes until interrupted. |
| `--lines N` | `-n` | Number of past processes to print. Default `100`; `0` prints none, `-1` all. |
| `--json` | | Print the shown records as JSON lines, unchanged. |
| `--all` | | Also show jailoc's own guest processes. |

Without `--json`, control characters in logged values are escaped. The record is written inside the VM: root in the VM (`sudo`) can stop or alter it. Processes that exit before the guest reads their details show only their name.

---

### `jailoc exec`

Run a command as the agent user in a running workspace VM.

```
jailoc exec [flags] -- COMMAND [ARG...]
```

The command runs with the session environment and secrets, in the current directory when a workspace path contains it, otherwise in the first workspace path. When standard input is a terminal, the command gets one, including window size changes. The command's exit status becomes jailoc's exit status. Flags must come before the command.

---

### `jailoc shell`

Open a login shell as the agent user in a running workspace VM, in the same directory as `jailoc exec`. The shell is not a detachable session: closing the terminal ends it.

```
jailoc shell [workspace] [flags]
```

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
