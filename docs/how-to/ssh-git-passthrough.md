# How to pass through SSH and Git config

This guide shows how to give the agent your SSH agent, Git configuration and SSH known hosts inside the workspace VM, for example to clone private repositories, push over SSH or sign commits.

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

jailoc prints a warning and copies nothing for a referenced file that is missing, lies outside your home directory, is larger than 1 MiB, or is in `~/.ssh`, `~/.gnupg` or `~/.aws` (except a valid allowed signers file and an SSH public signing key). These checks apply to the file a symlink points to. The Git credential stores `~/.git-credentials` and `git/credentials` are never copied.

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
