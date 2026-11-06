# How to pass through SSH and Git config

This guide shows how to give the agent your SSH agent, GPG agent, Git configuration and SSH known hosts inside the workspace VM, for example to clone private repositories, push over SSH or sign commits.

## Enable SSH agent forwarding

Set `ssh_auth_sock = true` in `[defaults]` or in a workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
ssh_auth_sock = true
```

jailoc relays the host agent named by `SSH_AUTH_SOCK` into the VM. Sessions see it at `/run/ssh-agent.sock` with `SSH_AUTH_SOCK` set, so `git@host:repo` remotes and `ssh` work with your keys. Private keys stay on the host.

With forwarding on, `~/.ssh/known_hosts` (if it exists) is copied to `/home/agent/.ssh/known_hosts` when the VM starts and on every `jailoc reload`, so known Git remotes don't trigger host verification prompts.

jailoc reads `SSH_AUTH_SOCK` when it starts or reloads the VM. After the host agent moves, for example in a fresh login session, run this from a terminal with the current `SSH_AUTH_SOCK`:

```bash
jailoc reload my-project
```

!!! warning
    `SSH_AUTH_SOCK` is a reserved environment variable. Setting it in `env` or `env_file` fails validation. Use `ssh_auth_sock = true` instead.

---

## Enable GPG agent forwarding

Set `gpg_agent = true` in `[defaults]` or in a workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
gpg_agent = true
```

jailoc relays the extra socket of the host gpg-agent (`gpgconf --list-dirs agent-extra-socket`) into the VM at `/run/user/<uid>/gnupg/S.gpg-agent`, where `gpg` looks for its agent. If the host agent isn't running, jailoc starts it with `gpgconf --launch gpg-agent`. Secret keys stay on the host, and passphrase prompts appear on the host through its pinentry.

When the VM starts and on every `jailoc reload`, jailoc imports public keys into the VM keyring: the key named by `user.signingKey` in your global Git config, or the public keys of all host secret keys when it is unset. With [Git configuration copying](#copy-your-git-configuration) on, `git commit -S` and `commit.gpgSign = true` sign with your key.

The `gpg` binary must exist in the workspace image. The base image includes it.

!!! warning
    The forwarded agent signs and decrypts with any key it holds, for as long as forwarding is on. Passphrases cached by the host agent apply to requests from the VM too.

!!! note
    A `GNUPGHOME` set in `env` makes `gpg` look for its agent socket elsewhere, so the forwarded agent isn't found.

---

## Copy your Git configuration

jailoc copies your Git configuration into the VM by default, as read-only snapshots taken when the VM starts and on every `jailoc reload`:

| Host file | Guest file |
|---|---|
| `~/.gitconfig` | `/home/agent/.gitconfig` |
| `$XDG_CONFIG_HOME/git/config` (`~/.config/git/config` when `XDG_CONFIG_HOME` is unset) | `/home/agent/.config/git/config` |
| `$XDG_CONFIG_HOME/git/ignore` | `/home/agent/.config/git/ignore` |
| `$XDG_CONFIG_HOME/git/attributes` | `/home/agent/.config/git/attributes` |

Missing files are skipped. jailoc also follows the files these configurations reference and copies those under your home directory to the same place under `/home/agent`:

- `include.path` and `includeIf.<condition>.path`, recursively
- `core.excludesFile`, `core.attributesFile`, `commit.template`
- `gpg.ssh.allowedSignersFile`, reduced to its signer lines
- `user.signingKey` when it names a file holding an SSH public key, reduced to that key

In the copies, paths under your home directory are rewritten to `~/...`, and `includeIf "gitdir:~/..."` conditions are rewritten to your host home directory, because workspace paths keep their host location inside the VM.

jailoc prints a warning and copies nothing for a referenced file that is missing, lies outside your home directory, is larger than 1 MiB, or is in `~/.ssh`, `~/.gnupg` or `~/.aws` (except a valid allowed signers file and an SSH public signing key). The home directory check uses the path as written, so a symlink in your home directory to a dotfiles checkout works. The credential directory checks apply to both the path and the file a symlink points to, and catch hard links to files in those directories and in the directories symlinks there point to. A relative `include.path` must stay in your home directory or `$XDG_CONFIG_HOME`. The Git credential stores `~/.git-credentials` and `git/credentials` are never copied.

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

---


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

---

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

---

For every configuration field, see [Configuration reference](../reference/configuration.md).
