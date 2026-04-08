# How to pass through SSH and Git config

This guide shows how to give the agent access to your SSH agent, Git configuration, and SSH known hosts inside the container. This is useful when the agent needs to clone private repositories or push commits over SSH.

## Enable SSH agent forwarding

Set `ssh_auth_sock = true` in `[defaults]` or in a specific workspace:

```toml
[defaults]
ssh_auth_sock = true
```

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
ssh_auth_sock = true
```

jailoc auto-detects the SSH agent socket location:

1. On **Docker Desktop** and **OrbStack**, it uses the magic socket at `/run/host-services/ssh-auth.sock`.
2. On **native Linux**, it reads the `SSH_AUTH_SOCK` environment variable and mounts that socket.

If no valid socket is found, the mount is silently skipped — the container starts without SSH agent access.

The socket is mounted at `/run/ssh-agent.sock` inside the container and `SSH_AUTH_SOCK` is set automatically.

When SSH agent forwarding is enabled, `~/.ssh/known_hosts` is also mounted read-only into the container (if the file exists on the host). This prevents SSH host verification prompts when connecting to known Git remotes.

!!! warning
    `SSH_AUTH_SOCK` is a reserved environment variable. Setting it manually in `env` or `env_file` causes a validation error. Use `ssh_auth_sock = true` instead.

---

## Disable Git configuration mounting

Git configuration (`~/.gitconfig` or `~/.config/git/config`) is mounted read-only into the container by default. To disable it for a specific workspace:

```toml
[workspaces.isolated]
paths = ["~/projects/isolated"]
git_config = false
```

Or globally:

```toml
[defaults]
git_config = false
```

jailoc checks for a Git config file in this order:

1. `~/.gitconfig`
2. `~/.config/git/config` (XDG location)

The first file found is mounted at `/home/agent/.gitconfig` inside the container. If neither exists, the mount is skipped.

---

## Enable everything at once

To forward SSH agent and keep Git config mounted (the default) for every workspace:

```toml
[defaults]
ssh_auth_sock = true
```

Individual workspaces can override any of these defaults:

```toml
[workspaces.public-only]
paths = ["~/projects/public-only"]
ssh_auth_sock = false
git_config = false
```

---

## Per-workspace overrides

Each workspace can override the defaults. Workspace-level settings take precedence:

```toml
[defaults]
ssh_auth_sock = true

[workspaces.private-repos]
paths = ["~/projects/private-repos"]
# inherits ssh_auth_sock = true from defaults

[workspaces.isolated]
paths = ["~/projects/isolated"]
ssh_auth_sock = false  # disables SSH for this workspace only
git_config = false     # disables Git config for this workspace only
```

When a workspace does not set a field, it inherits the value from `[defaults]`. For `git_config`, the fallback is `true` when neither the workspace nor defaults set it. When set explicitly, the workspace value wins.

---

For a full list of all configuration fields and their types, see [Configuration reference](../reference/configuration.md).
