# How to configure workspaces

This guide shows how to define and tune workspaces in jailoc's configuration file. For every field, its type and its validation rules, see the [Configuration reference](../reference/configuration.md).

Separate guides cover [network access](network-access.md) (`allowed_hosts`, `allowed_networks`), [custom images](custom-images.md) (`image`, `dockerfile`, `build_context`), [SSH and Git passthrough](ssh-git-passthrough.md) (`ssh_auth_sock`) and [coding agents](coding-agents.md) (`agent`, `agents`).

## Find the config file

jailoc stores its configuration at:

```
~/.config/jailoc/config.toml
```

The file is created on first run with a `default` workspace already in place. Edit it with any text editor.

## Select a workspace

Commands use the workspace whose `paths` contain the current directory, the most specific one if several do, and fall back to `default`. Given these two workspaces:

```toml
[workspaces.broad]
paths = ["/home/you/projects"]

[workspaces.specific]
paths = ["/home/you/projects/api"]
```

Running `jailoc up` from `/home/you/projects/api/src` selects `specific`. Running it from `/home/you/projects/other` selects `broad`. To pick a workspace explicitly, pass its name:

```bash
jailoc up api
jailoc -w api sessions
```

See [Workspace resolution](../reference/cli.md#workspace-resolution) for the exact order.

## Define a workspace

Each workspace is a `[workspaces.<name>]` section. Most workspaces set `paths`; the first path is the working directory. A workspace without `paths` starts in `/home/agent`:

```toml
[workspaces.default]
paths = ["/home/you/projects/myproject"]
```

Each path is shared into the VM read-write at its original absolute path, so `/home/you/projects/myproject` has the same path inside the guest. Files the agent creates there belong to your host user.

!!! note
    `~` expands to your home directory. Paths under system directories (`/usr`, `/etc`, `/var`, `/home/agent`, `/tmp` and similar) are rejected.

## Give a workspace several paths

List more than one directory when the agent needs several repositories at once:

```toml
[workspaces.api]
paths = [
  "/home/you/projects/api",
  "/home/you/projects/shared-libs",
]
```

Each workspace gets its own VM. Define as many `[workspaces.<name>]` sections as you need.

## Set environment variables

Pass variables to agent sessions with `env`, or load them from files with `env_file`. Env files must exist at config load time and use `.env` format (`KEY=VALUE`, `#` comments, quoted values):

```toml
[defaults]
env = ["GOPRIVATE=*.example.com"]
env_file = ["~/.config/jailoc/shared.env"]

[workspaces.api]
paths = ["/home/you/projects/api"]
env = ["LOG_LEVEL=debug"]
```

`env` entries override `env_file` entries with the same key.

!!! note
    Keys jailoc sets itself are rejected: `JAILOC`, `JAILOC_WORKSPACE`, `NODE_USE_SYSTEM_CA`, `SSH_AUTH_SOCK` and the agent variables listed in [Coding agents](../reference/configuration.md#coding-agents).

For tokens and passwords, use [secrets](secrets.md) instead of `env`.

## Share extra directories with mounts

`mounts` are live, two-way shares. Each entry has the form `host:guest[:mode]`, where `mode` is `ro` or `rw` (the default):

```toml
[defaults]
mounts = ["~/.config/ai-instructions:/home/agent/.config/ai-instructions:ro"]
```

Per-workspace `mounts` override `[defaults]` entries with the same guest path. To drop a default mount for one workspace, give it an empty host side:

```toml
[workspaces.my-project]
paths = ["/home/you/projects/my-project"]
mounts = [":/home/agent/.config/ai-instructions"]
```

!!! warning
    Mounts share directories only. A mount whose host side is a file fails `jailoc up` with `shares directories only; use snapshots`. Use [`snapshots`](#snapshot-files-into-the-workspace) for single files.

!!! note
    Dangerous host paths are rejected: `/`, `/boot`, `/dev`, `/etc`, `/private`, `/proc`, `/sys`, `/run`, `/var`, `~/.ssh`, `~/.gnupg`, `~/.aws`. Guest destinations under `/home/agent/...` are allowed; other system directories are rejected.

## Snapshot files into the workspace

For single files, or when the workspace should get a frozen copy instead of the host original, use `snapshots`:

```toml
[workspaces.myproject]
paths = ["~/src/myproject"]
snapshots = [
  "~/.npmrc:/home/agent/.npmrc",
  "~/.config/gh:/home/agent/.config/gh",
]
```

Each snapshot is copied when the VM starts and is read-only inside the guest, even for root. Nothing the workspace does reaches the host original. A symlink given as the snapshot source is resolved; symlinks inside a snapshotted directory are copied as symlinks. A snapshot destination can't overlap a shared path, so it can't point into a workspace path or a `mounts` destination. To pick up host edits, or added and removed entries, run `jailoc reload`.

## Publish ports

To reach a service in the VM from the host, publish its port:

```toml
[workspaces.myproject]
ports = [3000, "8080:80"]
```

`3000` makes guest port 3000 available on host `127.0.0.1:3000`. `"8080:80"` makes guest port 80 available on host `127.0.0.1:8080`. The service must listen on `0.0.0.0` (or the guest address `192.168.127.2`), not on `127.0.0.1`. Containers started with `docker run -p` inside the guest are reachable the same way.

## Protect Git hooks and configuration

Workspace paths are shared read-write, including `.git`. Git on the host runs hooks and commands named in `.git/config` (such as `core.fsmonitor`), so an agent that writes there can run code on your host. Prevent it with:

```toml
[workspaces.myproject]
protect_git = true
```

At every `jailoc up`, jailoc finds the repositories under the workspace paths, including symlinked workspace roots. Inside the VM, it makes `.git/hooks`, `.git/config` and linked worktree `commondir` pointers read-only, including metadata of submodules, worktrees and separate git directories reached through `.git` files under the workspace paths. These git directories and their parents cannot be renamed or removed. `sudo` and Docker are disabled in that workspace, and `enable_docker = true` together with `protect_git = true` is rejected.

!!! warning
    Only repositories that exist at `jailoc up` are covered. Repositories the agent creates or clones later aren't protected until the next restart. Files that tools run from the working tree, such as `Makefile` or `package.json` scripts, aren't covered either.

## Use Docker inside the VM

With `enable_docker = true` (the default), the guest runs its own Docker daemon when the image contains `dockerd`. The built-in image ships Docker CE with the `buildx` and `compose` plugins. Images, containers and volumes live on a 20 GiB data disk that survives `jailoc down`; `jailoc down --purge` deletes it.

A custom image without `dockerd` starts without Docker and prints a warning. Set `enable_docker = false` to silence it. With an explicit `enable_docker = true`, such an image fails `jailoc up`.

## Set CPU and memory

```toml
[defaults]
cpu = 2.0
memory = "4g"

[workspaces.heavy-agent]
cpu = 8.0
memory = "16g"
```

Workspace values override defaults. Without either, the VM gets 2 vCPUs and `4g`. `cpu` is rounded up to whole vCPUs. `memory` is a positive integer optionally followed by `k`, `m` or `g`, and must be at least `512m`. The limits apply to the whole VM, including its Docker daemon.

## Apply changes to a running workspace

After editing the config, apply it without restarting:

```bash
jailoc reload myproject
```

Network rules, DNS filtering, `ports`, `ssh_auth_sock`, `ca_bundle`, `snapshots`, `env`, `env_file` and secrets apply live. Running sessions keep their old environment. Other changes, such as `paths`, `mounts`, `cpu`, `memory`, `enable_docker`, `protect_git` and the image, need a new VM. `jailoc reload` lists them; apply them with `jailoc restart`, which ends running sessions.
