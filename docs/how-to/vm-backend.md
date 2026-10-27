# Run a workspace in a VM

!!! warning "Experimental"
    `backend = "vm"` runs on macOS (Apple Silicon) with libkrun. Linux support exists in the code but is not tested. Configuration and behaviour can change between releases.

With `backend = "vm"`, each workspace runs in its own lightweight virtual machine instead of on the host Docker daemon. The VM boots the workspace image as its root file system and the agent runs directly in it, with no container in between. The VM is the security boundary, and all network traffic leaves through a jailoc-owned network broker on the host. No host Docker installation is needed. See [VM Backend](../explanation/vm-backend.md) for the design.

## VM runtime

On macOS (Apple Silicon), jailoc needs nothing besides itself. The first `jailoc up` of a VM workspace downloads the pinned VM runtime (about 25 MB: the `go-microvm-runner` process that hosts the VM, libkrun, and the libkrunfw guest kernel), verifies its SHA-256 checksums and unpacks it into `~/.cache/jailoc-vm/runtime/`. Later starts reuse it.

To use your own runner and libraries instead, for example on Linux where no runtime is pinned, set them in [`[vm]`](../reference/configuration.md#vm):

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/homebrew/lib"
```

On Linux, the user running jailoc also needs read and write access to `/dev/kvm`.

jailoc ships `jailoc-guest`, the init process and SSH server of workspace VMs, inside its own binary. A jailoc built from source without `go generate ./internal/guestbin` (see [Development](../development.md)) has none and looks for `jailoc-guest` next to the runner instead.

## Migrate all workspaces

To move every existing workspace to the VM backend in one step, run:

```bash
jailoc migrate --dry-run   # preview
jailoc migrate
```

It asks how to carry over each mount (mount, copy or drop), removes settings the VM backend does not support after confirmation, stops running Docker workspaces, copies their OpenCode data into the VM, and keeps the previous configuration as `config.toml.pre-vm-migration`. See [`jailoc migrate`](../reference/cli.md#jailoc-migrate).

## Switch a workspace to the VM backend

Set `backend` on the workspace, or in `[defaults]` for all workspaces:

```toml
[workspaces.myproject]
backend = "vm"
paths = ["~/src/myproject"]
cpu = 2.0
memory = "4g"
```

Start it as usual:

```bash
jailoc up myproject
jailoc
```

The first `jailoc up` builds the workspace image exactly as the Docker backend would build it (see [Image Resolution](../reference/image-resolution.md)), with the host Docker daemon when one is available and otherwise in a builder VM. It then unpacks the image and boots the workspace VM from it. Later starts reuse the build cache and the unpacked image and take a few seconds.

`jailoc` then opens OpenCode inside the VM, in the directory you started it from. The VM keeps running until `jailoc down`.

## Detach and re-attach

OpenCode runs in a session that outlives your terminal:

- Press `Ctrl-\` or close the terminal window to detach. The agent keeps working.
- Run `jailoc` in the same directory to re-attach; the screen is redrawn.
- Run `jailoc --new` to start a second session next to the running one, and `jailoc sessions` to list them. `jailoc --attach ID` attaches to a specific one.
- Running `jailoc` from another terminal takes the session over; the first terminal is detached.
- Quitting OpenCode ends the session. `jailoc sessions stop ID` ends it from outside, and `jailoc down` ends all sessions of the workspace.

The whole VM, including its Docker daemon, gets `cpu` rounded up to whole vCPUs and `memory` as its memory; `memory` must be at least `512m`. Inside the VM there are no further limits.

## What the agent sees

- It runs as `agent` (UID 1000) and, unless `protect_git` is set, can become root with `sudo` when the image allows it (the built-in image does), for example to install packages with `apt`. Root inside the VM cannot reach host files outside the shares, write read-only shares, or bypass the network rules.
- Workspace `paths` and the directories of `mounts` appear at their configured paths. Files the agent creates in them belong to your host user.
- OpenCode sessions and logins (`~/.local/share/opencode`), its prompt history and model choice (`~/.local/state/opencode`), and `~/.cache` persist across `jailoc down` and `jailoc up` unless a mount covers them. `jailoc down --purge` deletes them. Each workspace has its own OpenCode login.
- Everything else outside the shares, including packages installed with `sudo apt`, is reset on every start; install lasting tools through the workspace Dockerfile.
- Secrets are in `/run/secrets/` (mode `0400`), and environment secrets are also set in the session environment.
- The Git configuration (`git_config`) is a copy at `~/.gitconfig`; a forwarded CA bundle is added to the system trust store.

## Copy files into the workspace

`mounts` are live, two-way shares and work on directories only. For single files, or whenever the workspace should get a snapshot instead of access to the host original, use `copies`:

```toml
[workspaces.myproject]
backend = "vm"
paths = ["~/src/myproject"]
copies = [
  "~/.npmrc:/home/agent/.npmrc",
  "~/.config/gh:/home/agent/.config/gh",
]
```

Each copy is taken when the VM starts and is read-only in the workspace, even for root. Changes on the host appear after `jailoc down` and `jailoc up`; nothing the workspace does reaches the host original.

A copy destination cannot overlap a shared path, so it cannot point into a workspace path or a `mounts` destination.

## Choose where images are built

`[vm] builder` selects where workspace images are built:

| Value | Builds with |
|-------|-------------|
| `"auto"` (default) | the host Docker daemon when it answers and builds Linux images for the host's CPU architecture, otherwise the builder VM |
| `"docker"` | the host Docker daemon; `jailoc up` fails when it is not usable |
| `"vm"` | always the builder VM |

```toml
[vm]
builder = "vm"
```

!!! warning
    With the host Docker daemon, Dockerfile `RUN` steps of workspace images run with the isolation of the Docker backend, not inside a VM, and the build reaches every network the host reaches. Use `builder = "vm"` or `isolated_build` for Dockerfiles you do not trust.

The builder VM is shared by all VM workspaces, keeps one build cache, and reaches public addresses only, regardless of `allowed_hosts` and `allowed_networks`.

To build a workspace's image in a builder VM of its own, with its own build cache and the workspace's network rules, set:

```toml
[workspaces.myproject]
isolated_build = true
```

Set `isolated_build = true` in `[defaults]` to isolate every workspace. `isolated_build` always uses a builder VM, whatever `[vm] builder` says. Each builder VM runs only while an image is built.

## Protect Git hooks and configuration

Workspace `paths` are shared read-write, including the `.git` directories of the repositories in them. Git on the host runs hooks from `.git/hooks` and commands named in `.git/config` (for example `core.fsmonitor`), so an agent that writes there can run code on the host the next time you use Git in that repository. To prevent it, set:

```toml
[workspaces.myproject]
protect_git = true
```

At every `jailoc up`, jailoc finds the Git repositories under the workspace `paths` and makes inside the VM:

- `.git/hooks`, `.git/config`, the same files of submodules, and `.git` files of worktrees and submodule checkouts read-only (a missing `hooks` directory is created first);
- the `.git` directories, the repositories and the directories between a workspace path and a repository impossible to rename or remove, so the agent cannot swap a protected `.git` for its own.

`sudo` and Docker are disabled in the workspace, because root inside the VM, which Docker access amounts to, could lift these protections. Setting `enable_docker = true` together with `protect_git = true` is rejected. The agent can still commit, branch, fetch and push; Git commands that change `.git/config`, such as `git remote add` or `git config`, fail.

The protection covers repositories that exist at `jailoc up`. Repositories created later, including those the agent clones, are not protected; restart the workspace to include them. Files in the working tree that tools run, such as `Makefile` or `package.json` scripts, are not covered either.

## Use Docker in the VM

With `enable_docker = true`, the default, the workspace VM runs its own Docker daemon when the workspace image contains `dockerd`. The built-in image ships Docker CE with the `buildx` and `compose` plugins; a custom image needs Docker installed to use it. The agent reaches the daemon through `/var/run/docker.sock` without `sudo`, so `docker build`, `docker compose` and Testcontainers work inside the workspace.

Images, containers and volumes live on a 20 GiB data disk of the workspace VM and persist across `jailoc down` and `jailoc up`; `jailoc down --purge` deletes them. Containers get bridge networking, and their traffic passes the same broker rules as the agent's.

A workspace image without `dockerd` starts without Docker and prints a warning; set `enable_docker = false` to silence it. With an explicit `enable_docker = true`, such an image fails `jailoc up`.

## Publish ports

To reach a service in the VM from the host, publish its port:

```toml
[workspaces.myproject]
ports = [3000, "8080:80"]
```

`3000` makes guest port 3000 available on host `127.0.0.1:3000`; `"8080:80"` makes guest port 80 available on host `127.0.0.1:8080`. Ports are bound when the VM starts, so changing `ports` restarts the VM at the next `jailoc up`. The service must listen on `0.0.0.0` (or the VM address `192.168.127.2`), not on `127.0.0.1`. Docker containers published with `docker run -p 3000:3000` are reachable the same way.

## Run background processes

The VM has no init system: nothing restarts a process that exits, and every process ends with the VM. To keep a dev server or watcher running:

- run it in its own session: `jailoc --new` opens a second session, start the server there and detach with `Ctrl-\`;
- or start it detached from a shell, for example `setsid npm run dev > /tmp/dev.log 2>&1 &`;
- or run it as a Docker container with `docker run -d --restart unless-stopped`, which Docker restarts when it exits.

## Use your SSH agent

With `ssh_auth_sock = true`, jailoc relays the host SSH agent named by `SSH_AUTH_SOCK` into the VM: sessions see it at `/run/ssh-agent.sock`, so `git@host:repo` remotes and `ssh` work with your keys. The private keys stay on the host. `~/.ssh/known_hosts` is copied to the agent's home at `jailoc up`.

The agent is available as long as the VM runs, also to detached sessions and to containers that mount `/run/ssh-agent.sock`. jailoc reads `SSH_AUTH_SOCK` when it starts the VM; after the host agent moves (for example a new login session), run `jailoc restart`.

## Filter DNS

`filtered_dns`, `dns_upstream` and `dns_blocked_zones` work as with the Docker backend, enforced by the network broker: names in the blocked zones get NXDOMAIN, other names are resolved through `dns_upstream`, and DNS (port 53) to any other resolver is blocked. The shared builder VM does not filter DNS; use `isolated_build = true` to apply the filter to builds.

## Stop and remove the VM

```bash
jailoc down myproject           # stop the VM, keep home directories and build cache
jailoc down myproject --purge   # also delete the VM state, home directories, Docker data and isolated builder
```

## Allow private hosts and networks

`allowed_hosts` and `allowed_networks` become rules of the network broker:

- `allowed_networks` entries open the CIDR for all ports. IPv6 entries are ignored, because the VM has no IPv6 connectivity.
- `allowed_hosts` names are resolved on the host when the VM starts. Their IPv4 addresses are allowed for all ports. Restart the workspace after the addresses change.

Host addresses, the broker gateway and cloud metadata endpoints stay blocked regardless of these settings.

## Restrictions

- Sessions use exec mode only; `jailoc --remote` is rejected and `expose_port = true` is rejected.
- The VM kernel is the libkrun kernel: no `systemd` or other init system, no loadable kernel modules, no swap, no nested virtualization (`/dev/kvm`), and `dmesg` is not readable.
- The built-in image has no compilers or extra debugging tools such as `gcc`, `pip` or `strace`. Install them with `sudo apt-get install` for the current run, or add them to the workspace Dockerfile to keep them.
- `mounts` must name directories. A file mount is rejected at `jailoc up`; use `copies` for files.
- The shared builder VM reaches public addresses only. Images that pull from private registries or hosts need `isolated_build = true` with the hosts in `allowed_hosts`, or `[vm] builder = "docker"`.
- On macOS, reaching hosts on the local network through `allowed_hosts` or `allowed_networks` needs the macOS Local Network permission for the process that runs jailoc. Without it, connections to local-network addresses fail with `no route to host`.
- IPv6 is not available inside the VM.

## Inspect a running VM

Workspace VM state lives in `~/.cache/jailoc-vm/workspaces/<workspace>/`. `jailoc logs` prints the two log files:

| File | Content |
|------|---------|
| `supervisor.log` | VM supervisor output: broker and VM start, readiness, shutdown reason |
| `microvm/console.log` | Guest console: boot and SSH connections |
| `broker.jsonl` | Network broker decisions, one JSON object per line |
| `state.json` | Supervisor PID and readiness |
| `home/` | Persistent home directories of the agent |

The shared builder lives in `~/.cache/jailoc-vm/builder/`, an isolated one in `~/.cache/jailoc-vm/workspaces/<workspace>/builder/`.

Unpacked workspace images live in `~/.cache/jailoc-vm/images/`. Each `jailoc up` removes images that no workspace uses any more and extracted layers unused for two weeks; entries changed within the last hour are kept, because another `jailoc up` may still need them.
