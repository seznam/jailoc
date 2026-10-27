# Getting Started

This tutorial walks you through installing jailoc and running your first sandboxed workspace. By the end, you'll have an OpenCode agent running in an isolated micro-VM, working on your project directory.

## Prerequisites

You need one of these hosts:

- **macOS on Apple Silicon**. jailoc uses Hypervisor.framework.
- **Linux on amd64** with KVM. Your user needs read and write access to `/dev/kvm`.

You also need **Docker Engine** running on the host. jailoc uses it to build the workspace image, which it then exports into the VM. Verify Docker is running:

```bash
docker info
```

## Install jailoc

Install the latest release with `go install`:

```bash
go install github.com/seznam/jailoc/cmd/jailoc@{{ version }}
```

Alternatively, download a pre-built binary from [GitHub Releases](https://github.com/seznam/jailoc/releases).

After installation, confirm it's on your `PATH`:

```bash
jailoc --version
```

## Run your first workspace

The quickest way to get started is to run `jailoc` without any arguments from your project directory:

```bash
cd ~/myproject
jailoc
```

Here's what happens on first run:

1. **Config creation**: jailoc writes a default config to `~/.config/jailoc/config.toml` if it doesn't exist yet.
2. **Workspace prompt**: if the current directory isn't part of any configured workspace, jailoc asks whether to add it. Confirm, and the workspace is saved to your config.
3. **Runtime download**: jailoc downloads its pinned VM runtime and checks its SHA-256 digest. This happens once.
4. **Image build**: jailoc builds the workspace image with Docker and unpacks it into a VM root filesystem.
5. **VM start**: jailoc boots the workspace VM, shares your project directory into it, and starts the network broker.
6. **Attach**: jailoc opens an agent session over SSH and attaches your terminal to it.

!!! note
    Your project directory is shared at its original path inside the VM. If your project lives at `/home/you/myproject`, the agent sees it at the same path. Running `jailoc` from a subdirectory (for example `~/myproject/src`) opens that subdirectory in the agent instead of the workspace root.

## Detach and come back

Press `Ctrl-\` to detach from the session. The agent keeps running inside the VM. Run `jailoc` again from the same directory to reattach to it.

To see the sessions running in the workspace:

```bash
jailoc sessions
```

## Use subcommands directly

The bare `jailoc` shortcut is convenient, but you can also drive each step explicitly:

```bash
# Start the workspace VM in the background
jailoc up
```

This separation is useful when you want to start the workspace and come back to it later.

Other subcommands worth knowing:

```bash
jailoc status   # show the state of the workspace VM
jailoc logs     # show workspace logs
jailoc down     # stop the workspace VM
```

`jailoc down` keeps the VM's home state, such as OpenCode sessions and caches, for the next start. `jailoc down --purge` deletes it.

## What's running

When the workspace is up, these pieces are active:

- **The workspace VM** runs `jailoc-guest` as its init. It serves SSH to jailoc, and each agent session runs as the `agent` user (UID 1000), which can use `sudo` inside the VM.
- **The network broker** runs on the host. All of the VM's traffic goes through it. Public destinations are reachable, but private and reserved address ranges are blocked unless you allow them.

## Next steps

Now that you have a workspace running, you can dig into configuration:

- [Workspace Configuration](../how-to/workspace-configuration.md): add more workspaces, set paths, and tune per-workspace options
- [Sessions](../how-to/sessions.md): start new sessions, continue old ones, and attach by ID
- [Network Access](../how-to/network-access.md): allow specific internal hosts or CIDR ranges
- [Custom Images](../how-to/custom-images.md): add your own toolchain to the workspace image
- [Configuration Reference](../reference/configuration.md): full reference for every config field
