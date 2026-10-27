# How to pass through SSH and Git config

This guide shows how to give the agent your SSH agent, Git configuration and SSH known hosts inside the workspace VM, for example to clone private repositories or push over SSH.

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

## Disable Git configuration copying

jailoc copies your Git config into the VM at `/home/agent/.gitconfig` by default. It uses the first file it finds:

1. `~/.gitconfig`
2. `~/.config/git/config`

If neither exists, nothing is copied. The copy is a read-only snapshot taken when the VM starts, so host edits show up after `jailoc reload`.

To turn it off for one workspace:

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
