# How to pass through SSH and Git config

This guide shows how to give the agent your SSH agent, GPG agent, Git configuration and SSH known hosts inside the workspace VM, for example to clone private repositories, push over SSH or sign commits. For every configuration field, see [Configuration reference](../reference/configuration.md).

## Enable SSH agent forwarding

Set `ssh_auth_sock = true` in `[defaults]` or in a workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
ssh_auth_sock = true
```

!!! warning
    `SSH_AUTH_SOCK` is a reserved environment variable. Setting it in `env` or `env_file` fails validation. Use `ssh_auth_sock = true` instead.

jailoc relays the host agent named by `SSH_AUTH_SOCK` into the VM. Sessions see it at `/run/ssh-agent.sock` with `SSH_AUTH_SOCK` set, so `git@host:repo` remotes and `ssh` work with your keys. Private keys stay on the host.

With forwarding on, `~/.ssh/known_hosts` (if it exists) is copied to `/home/agent/.ssh/known_hosts` when the VM starts and on every `jailoc reload`, so known Git remotes don't trigger host verification prompts.

jailoc reads `SSH_AUTH_SOCK` when it starts or reloads the VM. After the host agent moves, for example in a fresh login session, run this from a terminal with the current `SSH_AUTH_SOCK`:

```bash
jailoc reload my-project
```

## Enable GPG agent forwarding

!!! warning
    The forwarded agent signs and decrypts with any key it holds, for as long as forwarding is on. Passphrases cached by the host agent apply to requests from the VM too.

Set `gpg_agent = true` in `[defaults]` or in a workspace. The `gpg` binary must exist in the workspace image; the base image includes it.

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
gpg_agent = true
```

jailoc relays the extra socket of the host gpg-agent (`gpgconf --list-dirs agent-extra-socket`) into the VM at `/run/user/<uid>/gnupg/S.gpg-agent`, where `gpg` looks for its agent. If the host agent isn't running, jailoc starts it with `gpgconf --launch gpg-agent`. Secret keys stay on the host, and passphrase prompts appear on the host through its pinentry.

When the VM starts and on every `jailoc reload`, jailoc imports public keys into the VM keyring: the key named by `user.signingKey` in your global Git config, or the public keys of all host secret keys when it is unset. With [Git configuration copying](#copy-your-git-configuration) on, `git commit -S` and `commit.gpgSign = true` sign with your key.

!!! note
    A `GNUPGHOME` set in `env` makes `gpg` look for its agent socket elsewhere, so the forwarded agent isn't found.

## Copy your Git configuration

jailoc copies your Git configuration into the VM by default, as read-only snapshots taken when the VM starts and on every `jailoc reload`: `~/.gitconfig`, and `config`, `ignore` and `attributes` from `$XDG_CONFIG_HOME/git` (`~/.config/git` when unset). It also copies the files these configurations reference (includes, `core.excludesFile`, `commit.template` and similar) when they are under your home directory.

jailoc prints a warning and skips a referenced file that is missing, outside your home directory or over 1 MiB, and one in `~/.ssh`, `~/.gnupg` or `~/.aws` unless it is a valid SSH public signing key or allowed-signers file (see the [exceptions](../reference/configuration.md#git_config)). The Git credential stores, such as `~/.git-credentials`, are never copied. The exact file list, path rewrites and checks are in [`git_config`](../reference/configuration.md#git_config).

!!! note
    SSH commit signing needs the private key, which stays on the host. Point `user.signingKey` at the public key file (for example `~/.ssh/id_ed25519.pub`) and enable `ssh_auth_sock` so `ssh-keygen` signs through the agent.

To copy other files or a different configuration, add [`snapshots`](../reference/configuration.md#snapshots). A snapshot with the same destination replaces the default copy, and one with an empty host removes it:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
snapshots = [
  "~/work.gitconfig:~/.gitconfig",
  ":~/.config/git/ignore",
]
```

## Disable Git configuration copying

To turn the copies off for one workspace:

```toml
[workspaces.isolated]
paths = ["~/projects/isolated"]
git_config = false
```

Or for all workspaces:

```toml
[defaults]
git_config = false
```

## Per-workspace overrides

Workspace settings win over `[defaults]`. A workspace that doesn't set a field inherits it, and `git_config` falls back to `true` when neither sets it:

```toml
[defaults]
ssh_auth_sock = true

[workspaces.private-repos]
paths = ["~/projects/private-repos"]
# inherits ssh_auth_sock = true

[workspaces.isolated]
paths = ["~/projects/isolated"]
ssh_auth_sock = false
git_config = false
```
