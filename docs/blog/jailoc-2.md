# jailoc 2: every agent gets its own computer

*November 2026 · release 2.0*

jailoc 1 put your coding agent in a Docker container. That was a good fence, but it was a fence made of the host's own kernel, with the Docker daemon on the other side of it. jailoc 2 removes the shared kernel: every workspace boots its own micro-VM, and the agent lives there. This post is a tour of everything that changed along the way.

**TL;DR**

- Every workspace is a [libkrun](https://github.com/containers/libkrun) micro-VM with its own kernel, on macOS (Apple Silicon) and Linux (KVM).
- Pick your agent: OpenCode 2, OpenCode 1, Claude Code, GitHub Copilot CLI or OmO, or a variant you define yourself.
- Sessions detach and reattach like tmux, and survive closed terminals.
- A host-side network broker sees every packet. `jailoc traffic` shows you where the agent went, and `jailoc strace` shows what it ran.
- Most config changes apply to a running VM with `jailoc reload`.
- Coming from v1? Run `jailoc migrate`.

## A VM per workspace, not a container

Each workspace gets a VM booted with `jailoc-guest` as its init. Your project directories are shared in over virtio-fs at their original paths. Everything else on the host, like `~/.ssh`, your browser profiles and other projects, simply doesn't exist inside.

You don't install a hypervisor stack. Release binaries embed the VM runtime (the VM runner, `libkrun` and `libkrunfw`) and unpack it on first use, so nothing is downloaded. Docker on the host is only needed to build workspace images. With `isolated_build = true`, even that moves into a builder VM of its own.

```bash
jailoc up my-project    # build the image, boot the VM
jailoc                  # open the agent in the current directory
```

The first start builds the image. Later starts reuse the build cache and take a few seconds.

Read more: [Architecture](../explanation/architecture.md), [Overview](../explanation/overview.md).

## Bring your own agent

jailoc 2 is no longer an OpenCode-only tool. Set `agent` per workspace:

| `agent` | Runs |
|---|---|
| `opencode` / `opencode2` (default) | OpenCode 2 |
| `opencode1` | OpenCode 1 |
| `claude` | Claude Code |
| `copilot` | GitHub Copilot CLI |
| `omo` | OmO |

Each agent runs in full-autonomy mode, since the VM is the guardrail. Need a twist? Pin a version, add arguments, or derive a custom agent with its own name:

```toml
[defaults.agents.claude-opus]
from = "claude"
args = ["--model", "opus"]

[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude-opus"
```

Agent state such as logins and conversation history lives in a persistent home that survives VM restarts. By default each workspace has its own. With `profile = "<name>"`, several workspaces can share one; with `profile = false`, every start is a clean slate. Several workspaces can also point at the same project, so you can run Claude Code and Copilot CLI side by side on one repository.

Every agent shows a workspace indicator, so you always know which sandbox you're typing into.

Read more: [Coding Agents](../how-to/coding-agents.md), [Workspace Indicator](../how-to/tui-plugin.md).

## Sessions that outlive your terminal

The agent runs in a detachable session inside the VM. Press `Ctrl-b`, then `d` (configurable with `detach_keys`), or simply close the terminal: the agent keeps working. Run `jailoc` in the same directory and you're back where you left off. Attaching from a second terminal takes the session over.

```bash
jailoc sessions              # what's running in this workspace?
jailoc --attach 3f2a         # jump into a specific session
jailoc -c                    # new session continuing the last conversation
jailoc sessions stop 3f2a    # end one
```

Read more: [Sessions](../how-to/sessions.md).

## A network broker with opinions

Every packet the VM sends goes through `jailoc-netbroker`, a userspace network stack built on gVisor that runs on the host, one per workspace. Public internet: allowed. Private and reserved ranges (your clusters, databases and cloud metadata endpoints), over IPv4 and IPv6 alike: denied, unless you list them in `allowed_hosts` or `allowed_networks`. Add `filtered_dns` and internal zones stop resolving at all.

Two escape hatches, both explicit:

- `ports` publishes a guest port on host `127.0.0.1`, for the dev server the agent just started.
- `host_ports` relays a host loopback port or Unix socket into the guest, for a local MCP server for example. jailoc warns when you point it at something dangerous like Chrome DevTools or `docker.sock`.

Read more: [Network Access](../how-to/network-access.md), [Network Isolation](../explanation/network-isolation.md).

## See what the agent did

Sandboxing is good. Knowing what happened inside the sandbox is better.

```bash
jailoc traffic -f    # live connections, DNS answers, TLS server names, HTTP method + path
jailoc strace -f     # every process started in the VM, with its command line
```

`traffic` reads the broker's log, so it also lists denied connections and what was blocked by DNS filtering. `strace` records every process start from boot. For the times you want to look yourself, `jailoc exec -- <cmd>` runs a command as the agent user, and `jailoc shell` drops you into a login shell.

Read more: [CLI Reference](../reference/cli.md#jailoc-traffic).

## Change config without rebooting

Edited `config.toml`? `jailoc reload` applies the network policy, published ports, the SSH agent, the CA bundle, snapshots, `env` and secrets to the running VM, without interrupting the agent. Settings that really need a new VM (shares, CPU, memory, Docker) are listed, and `jailoc restart` applies them.

`jailoc status` tells you when the running VM has drifted from the config, along with the agent version, resources and session count. `jailoc list` gives the whole fleet at a glance, and `--all` works on `up`, `down`, `restart`, `reload` and `status`.

```text
  WORKSPACE  AGENT      STATE    SESSIONS        PATHS
  default    opencode   running  0               ~/projects
* myproject  claude     running  2 (1 attached)  ~/projects/myproject
  scratch    opencode1  stopped  -               -
```

Read more: [Workspace Configuration](../how-to/workspace-configuration.md).

## Docker, but inside the box

Agents love `docker run`. Handing them the host's `docker.sock` hands them the host. With `enable_docker = true`, jailoc runs `dockerd` inside the workspace VM, so containers the agent starts stay in the sandbox. It's opt-in, and the image only gets Docker when you ask for it, which keeps everyone else's image lean.

## Guard your Git

A compromised agent doesn't need to exfiltrate anything if it can plant a Git hook that runs on your host the next time you commit. `protect_git = true` makes `.git/hooks`, `.git/config` and worktree pointers read-only inside the VM, and turns off `sudo` and Docker so they can't be remounted.

## Secrets that don't touch the disk

Secrets come from a host environment variable, a file, or a command such as your password manager:

```toml
[workspaces.my-project.secrets.env.NPM_TOKEN]
from_command = "op read op://dev/npm/token"
```

jailoc reads them on the host and pushes them over SSH. Env secrets go only into the session environment; file secrets land in `/run/secrets` on tmpfs. Rotate them with `jailoc reload`.

Read more: [Secrets](../how-to/secrets.md), [Secrets architecture](../explanation/secrets.md).

## Small things that add up

- **Memory that copes:** zram swap and periodic reclaim in the guest, and the agent is the last process the OOM killer picks.
- **`sudo` in the VM:** the agent can `apt-get install` what it needs, inside its own machine.
- **Read-only snapshots:** copy files such as your Git config or `known_hosts` into the VM instead of sharing them live.
- **Faster builds:** BuildKit with cached apt and npm downloads, plus pruning of images you no longer use.
- **Another config file:** `--config` or `JAILOC_CONFIG` points jailoc at a different `config.toml`.

## Upgrading from v1

!!! warning "Breaking changes"

    - The Docker backend, remote mode and the published OpenCode port are gone. v1 settings (`mode`, `password_mode`, `expose_port`) fail config load.
    - `mounts` must be directories: the VM shares directories only. Move single-file mounts to `snapshots`.
    - `enable_docker` defaults to `false`, and Docker runs inside the VM instead of a sidecar container.
    - The base image doesn't contain OpenCode; jailoc installs the selected agent in a separate layer.

One command handles most of it:

```bash
jailoc migrate --dry-run   # preview
jailoc migrate             # convert config, stop v1 containers, copy OpenCode data
```

Your OpenCode 1 sessions and logins carry over. Set `agent = "opencode1"` to keep using them.

Read more: [Migrate from jailoc v1](../how-to/migrate-from-v1.md).

## Try it

[Install jailoc](../how-to/installation.md), then follow [Getting Started](../tutorials/getting-started.md). Your first sandboxed agent is a couple of commands away.
