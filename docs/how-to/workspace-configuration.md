# How to configure workspaces

This guide shows how to define and tune workspaces in jailoc's configuration file. For every field, its type and its validation rules, see the [Configuration reference](../reference/configuration.md).

Separate guides cover [network access](network-access.md) (`allowed_hosts`, `allowed_networks`), [custom images](custom-images.md) (`image`, `dockerfile`, `build_context`), [SSH and Git passthrough](ssh-git-passthrough.md) (`ssh_auth_sock`, `gpg_agent`) and [coding agents](coding-agents.md) (`agent`, `agents`).

## Find the config file

jailoc reads:

```text
~/.config/jailoc/config.toml
```

The file is created on first run with a `default` workspace in place. Edit it with any text editor.

To use another file, pass `--config` or set `JAILOC_CONFIG`:

```sh
jailoc --config ~/work/jailoc.toml up
export JAILOC_CONFIG=~/work/jailoc.toml
```

The file must exist. Give its workspaces names that no other configuration file uses: VMs are identified by workspace name alone. See [File location](../reference/configuration.md#file-location).

## Select a workspace

Commands use the workspace whose `paths` contain the current directory, the most specific one if several do, and fall back to `default`. Given these two workspaces:

```toml
[workspaces.broad]
paths = ["/home/you/projects"]

[workspaces.specific]
paths = ["/home/you/projects/api"]
```

- `jailoc up` from `/home/you/projects/api/src` selects `specific`.
- `jailoc up` from `/home/you/projects/other` selects `broad`.

To pick a workspace explicitly, pass its name:

```bash
jailoc up api
jailoc -w api sessions
```

See [Workspace resolution](../reference/cli.md#workspace-resolution) for the exact order.

## Define a workspace

Each workspace is a `[workspaces.<name>]` section. Most workspaces set `paths`; the first path is the working directory. A workspace without `paths` starts in `/home/agent`.

```toml
[workspaces.default]
paths = ["/home/you/projects/myproject"]
```

Each path is shared into the VM read-write at its original absolute path, so `/home/you/projects/myproject` has the same path inside the guest. Files the agent creates there belong to your host user.

Each path must be an existing directory: create missing project directories or remove stale paths, otherwise `jailoc up` fails.

!!! note
    `~` expands to your home directory. System directories such as `/usr`, `/etc`, `/var`, `/home/agent` and `/tmp` are rejected; see [`paths`](../reference/configuration.md#paths).

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

Pass variables to agent sessions with `env`, or load them from files with `env_file`. Env files must exist at config load time and use `.env` format (`KEY=VALUE`, `#` comments, quoted values).

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
    Keys jailoc sets itself, such as `JAILOC_WORKSPACE` and `SSH_AUTH_SOCK`, are rejected. See [`env`](../reference/configuration.md#env) for the list.

For tokens and passwords, use [secrets](secrets.md) instead of `env`.

## Share extra directories with mounts

`mounts` are live, two-way shares of directories. Each entry has the form `host:guest[:mode]`, where `mode` is `ro` or `rw` (the default). On the guest side, `~` stands for `/home/agent`.

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

A read-write mount creates a missing host directory. A read-only mount with a missing host directory fails `jailoc up` with `does not exist`, so a typo in a path doesn't leave an empty share.

!!! note
    System directories and credential directories such as `~/.ssh`, `~/.gnupg`, `~/.aws` and `~/.kube` are rejected as host paths, and so are directories that contain them (`~`, `~/.config`, `~/.cache`). To give the agent one credential from such a directory, use a [file secret](secrets.md#pass-a-credential-file). The full list and the guest destination rules are in [`mounts`](../reference/configuration.md#mounts).

## Snapshot files into the workspace

For single files, or when the workspace should get a frozen copy instead of the host original, use `snapshots`:

```toml
[workspaces.myproject]
paths = ["~/src/myproject"]
snapshots = [
  "~/.npmrc:/home/agent/.npmrc",
  "~/.config/git/ignore:/home/agent/.config/git/ignore",
]
```

- Each snapshot is copied when the VM starts and is read-only inside the guest, even for root. Nothing the workspace does reaches the host original.
- Run `jailoc reload` to pick up host edits and added or removed entries.
- A destination can't overlap a shared path (a workspace path or a `mounts` destination). Symlink handling and the other rules are in [`snapshots`](../reference/configuration.md#snapshots).
- Credential directories such as `~/.config/gh` can't be snapshotted. For the GitHub CLI, pass a token as an env secret instead (see [Pass a credential file](secrets.md#pass-a-credential-file)).

## Publish ports

To reach a service in the VM from the host, publish its port:

```toml
[workspaces.myproject]
ports = [3000, "8080:80"]
```

`3000` makes guest port 3000 available on host `127.0.0.1:3000`; `"8080:80"` makes guest port 80 available on host `127.0.0.1:8080`. The service must listen on `0.0.0.0` (or the guest address `192.168.127.2`), not on `127.0.0.1`. Containers started with `docker run -p` inside the guest are reachable the same way.

## Reach host services

The network broker blocks the guest from the host. `host_ports` opens a hole for one host service, such as a local MCP server or a browser started with `--remote-debugging-port`.

!!! warning
    Each entry gives the agent the same access to that service as a local process on the host. A browser debugging port controls the browser with all its logins, and a container runtime socket is root on the host. jailoc warns about both. Add only services you would let the agent use.

```toml
[workspaces.myproject]
host_ports = [9222, "8931:3845", "7100:~/.cache/mcp.sock"]
```

`9222` relays guest port 9222 to host `127.0.0.1:9222`. `"8931:3845"` relays guest port 8931 to host `127.0.0.1:3845`. `"7100:~/.cache/mcp.sock"` relays guest port 7100 to a host Unix socket. The host service must listen on `127.0.0.1` (or the socket path).

In the guest, connect to `localhost:9222` (unless a guest process already listens on that port), `host.jailoc.internal:9222`, `host.docker.internal:9222` or `192.168.127.1:9222`. See [`host_ports`](../reference/configuration.md#host_ports) for reserved guest ports and merging.

For example, attach Playwright MCP to a host Chrome:

```bash
# on the host, with a dedicated profile
google-chrome --remote-debugging-port=9222 --user-data-dir="$HOME/.cache/jailoc-chrome"
# in the guest
npx @playwright/mcp --cdp-endpoint http://localhost:9222
```

To cut access, remove the entry and run `jailoc reload`. Open connections are closed.

## Protect Git hooks and configuration

Workspace paths are shared read-write, including `.git`. Git on the host runs hooks and commands named in `.git/config` (such as `core.fsmonitor`), so an agent that writes there can run code on your host. `protect_git` prevents it:

```toml
[workspaces.myproject]
protect_git = true
```

!!! warning
    `protect_git = true` disables `sudo` and Docker in that workspace. Combining it with `enable_docker = true` is rejected.

!!! warning
    Only repositories that exist at `jailoc up` are covered. Repositories the agent creates or clones later aren't protected until the next restart, and repositories inside dependency and cache directories such as `node_modules`, `.venv` and `.cache` aren't searched. Files that tools run from the working tree, such as `Makefile` or `package.json` scripts, aren't covered either. The protection is enforced inside the guest; it holds against the agent, not against an exploit of the guest kernel.

At every `jailoc up`, jailoc finds the repositories under the workspace paths and read-write `mounts` and makes their hooks and Git configuration read-only inside the VM. A missing `.git/config` is created empty, and these git directories can't be renamed or removed. `jailoc up` fails when it can't protect a file safely, for example because of a symbolic link on the path, an extra hard link or an oversized configuration. The complete list of protected files, skipped directories and failure conditions is in [`protect_git`](../reference/configuration.md#protect_git).

## Use Docker inside the VM

Docker is off by default. Turn it on for a workspace, or for all of them in `[defaults]`:

```toml
[workspaces.my-project]
enable_docker = true
```

!!! warning
    `enable_docker = true` together with `protect_git = true` is rejected. An image used directly (`image`) must contain `dockerd`; otherwise `enable_docker = true` fails `jailoc up`.

Changing `enable_docker` takes effect after `jailoc restart`, which rebuilds the image. In the images it builds, jailoc adds Docker CE with the `buildx` and `compose` plugins. The guest then runs its own Docker daemon.

Images, containers and volumes live on a data disk that survives `jailoc down`; `jailoc down --purge` deletes it. The disk is 20 GiB unless you set `docker_disk`:

```toml
[workspaces.myproject]
enable_docker = true
docker_disk = "100g"
```

The disk is a sparse file, so it takes host space only for data written to it. Raising `docker_disk` grows the disk and its file system after `jailoc restart`. The disk can't shrink; to make it smaller, delete it with `jailoc down --purge`. With `isolated_build`, the workspace's builder VM gets a data disk of the same size.

## Set CPU and memory

```toml
[defaults]
cpu = 2.0
memory = "4g"

[workspaces.heavy-agent]
cpu = 8.0
memory = "16g"
```

Workspace values override defaults. Without either, the VM gets 2 vCPUs and `4g`. `cpu` is rounded up to whole vCPUs. `memory` must be at least `512m`; see [`cpu`, `memory` and `docker_disk`](../reference/configuration.md#cpu-memory-and-docker_disk) for the format. The limits apply to the whole VM, including its Docker daemon.

Inside the VM, jailoc swaps to compressed memory (zram, half of `memory`) and trims part of the file cache every minute; freed pages are reported to the host through the balloon device. See [Resource use](../explanation/resource-use.md).

## Set the size, storage and expiry of /tmp

`/tmp` lives in the VM's memory, is limited to `1g` and removes entries unused for `8h`. Change these with `tmp_storage`, `tmp_size` and `tmp_ttl`, in `[defaults]` or per workspace:

```toml
[workspaces.build]
tmp_size = "4g"
```

With `tmp_storage = "ram"` (the default), `/tmp` counts against `memory`. A large `tmp_size` is only a cap, so keep `memory` big enough for what the agent really writes. For big builds or test artifacts, move `/tmp` to a disk of its own:

```toml
[workspaces.build]
tmp_storage = "disk"
tmp_size = "20g"
```

The disk is private to the workspace and starts empty at every VM start. It needs at least `16m`, and ext4 metadata makes the usable space somewhat smaller than `tmp_size`. The host stores only the blocks that were written. The image must contain `mkfs.ext4` (`e2fsprogs`); see [custom images](custom-images.md#know-what-the-image-must-provide).

To keep files in `/tmp` as long as the VM runs, disable the expiry:

```toml
[workspaces.build]
tmp_ttl = "0"
```

Use a shorter or longer duration (`"30m"`, `"24h"`) to tune it. Only `/tmp` is cleaned: a long job can still fill it before the TTL passes, and `/var/tmp`, the persistent home and a `TMPDIR` pointing elsewhere are left alone. Sessions get `TMPDIR`, `TMP`, `TEMP` and `GOTMPDIR` set to `/tmp` unless the image or your `env` sets them. `tmp_ttl` applies with `jailoc reload`; `tmp_storage` and `tmp_size` need `jailoc restart`. See the [reference](../reference/configuration.md#tmp_storage-tmp_size-and-tmp_ttl) for the cleanup rules, and `jailoc status` for how full `/tmp` is.

## Apply changes to a running workspace

After editing the config, apply it without restarting:

```bash
jailoc reload myproject
```

These apply live: network rules, DNS filtering, `ports`, `host_ports`, `ssh_auth_sock`, `gpg_agent`, `ca_bundle`, `snapshots`, `env`, `env_file`, `tmp_ttl` and secrets. Running sessions keep their old environment.

Other changes need a new VM: `paths`, `mounts`, `cpu`, `memory`, `docker_disk`, `tmp_storage`, `tmp_size`, `enable_docker`, `protect_git` and the image. `jailoc reload` lists them. Apply them with `jailoc restart`, which ends running sessions.

Concurrent reloads of one workspace run one at a time.
