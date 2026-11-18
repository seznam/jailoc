# Getting Started

This tutorial installs jailoc, starts a coding agent (OpenCode) on your project directory inside an isolated micro-VM, and shows you how to detach from it and come back.

## Prerequisites

You need one of these hosts:

- **macOS on Apple Silicon**. jailoc uses Hypervisor.framework.
- **Linux on amd64 or arm64** with KVM. Your user needs read and write access to `/dev/kvm`. ARM64 Linux support is experimental.

Docker Engine on the host is optional. When it's running, jailoc builds workspace images with it; otherwise it builds them in a separate builder VM.

## Install jailoc

Install with [Homebrew](https://brew.sh/) on macOS or Linux:

```bash
brew install --cask seznam/tap/jailoc
```

Or download the archive for your platform from [GitHub Releases](https://github.com/seznam/jailoc/releases), extract it, and place the `jailoc` binary on your `PATH`. For Nix or a source build, see [How to install jailoc](../how-to/installation.md). Confirm it's on your `PATH`:

```bash
jailoc --version
```

## Run your first workspace

From your project directory, run `jailoc` without arguments:

```bash
cd ~/myproject
jailoc
```

On the first run jailoc asks up to two questions:

1. **First-time setup**: if `~/.config/jailoc/config.toml` doesn't exist or still matches the unmodified default template, jailoc offers to run the interactive [`jailoc setup`](../reference/cli.md#jailoc-setup) wizard (alias `jailoc wizard`). It chooses your coding agent, Git/SSH/GPG forwarding, network policy and initial workspace path. Run `jailoc setup` any time to add another workspace.
2. **Workspace prompt**: if the current directory isn't part of any configured workspace, jailoc asks whether to add it. Confirm, and it's saved to your config.

jailoc then prepares the workspace and attaches your terminal to an agent session over SSH. The first run takes longer: jailoc unpacks the VM runtime embedded in its binary into `~/.cache/jailoc-vm/runtime/` (once) and builds the workspace image, with host Docker or in a builder VM. Later runs reuse both. The [startup sequence](../explanation/overview.md#the-startup-sequence) describes each step.

!!! note
    Your project directory is shared at its original path inside the VM. If your project lives at `/home/you/myproject`, the agent sees it at the same path. Running `jailoc` from a subdirectory (for example `~/myproject/src`) opens that subdirectory in the agent instead of the workspace root.

## Detach and come back

Press `Ctrl-b`, then `d`, to detach. The agent keeps running inside the VM. Run `jailoc` again from the same directory to reattach. To list the sessions in the workspace:

```bash
jailoc sessions
```

## Use subcommands directly

The bare `jailoc` shortcut starts the workspace VM when needed. To start it without attaching, and to inspect or stop it, use the subcommands ([CLI reference](../reference/cli.md)):

```bash
jailoc up       # start the workspace VM in the background
jailoc status   # show the state of the workspace VM
jailoc logs     # show workspace logs
jailoc down     # stop the workspace VM
```

`jailoc down` keeps the VM's home state, such as agent conversations, logins and caches, for the next start. `jailoc down --purge` deletes it.

## What's running

A VM runs on your host behind a host-side network broker. Public destinations are reachable; private and reserved address ranges are blocked unless you allow them. Each agent session runs as the `agent` user inside the VM, which can use `sudo` there. See [the agent and guest root](../explanation/security-boundaries.md#agent-and-guest-root) and [the guest and the network](../explanation/security-boundaries.md#guest-and-network).

## Next steps

- [Workspace Configuration](../how-to/workspace-configuration.md): add more workspaces, set paths, and tune per-workspace options
- [Sessions](../how-to/sessions.md): start new sessions, continue old ones, and attach by ID
- [Network Access](../how-to/network-access.md): allow specific internal hosts or CIDR ranges
- [Custom Images](../how-to/custom-images.md): add your own toolchain to the workspace image
- [Configuration Reference](../reference/configuration.md): full reference for every config field
