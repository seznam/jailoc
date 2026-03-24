# How to configure workspaces

This guide shows how to define and tune workspaces in jailoc's configuration file. For a full reference of every field and its type, see [Configuration reference](../reference/configuration.md).

## Config file location

jailoc stores its configuration at:

```
~/.config/jailoc/config.toml
```

The file is created automatically on first run with a `default` workspace already in place. You can edit it with any text editor.

---

## Define a workspace

Each workspace is a `[workspaces.<name>]` section. The only required field is `paths`.

```toml
[workspaces.default]
paths = ["/home/you/projects/myproject"]
```

The first entry in `paths` becomes the container's working directory. All paths are bind-mounted inside the container at their original absolute path, so `/home/you/projects/myproject` is accessible at the same path inside the container.

!!! note
    `~` is expanded to your home directory. Paths under system directories (`/usr`, `/etc`, `/var`, `/home/agent`, and similar) are rejected.

---

## Define multiple workspaces

Add as many `[workspaces.<name>]` sections as you need:

```toml
[workspaces.api]
paths = ["/home/you/projects/api", "/home/you/projects/shared-libs"]

[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
```

Each workspace gets its own isolated container environment.

### Port allocation

jailoc assigns ports starting at `4096`, sorted alphabetically by workspace name. Given the example above:

| Workspace  | Port |
|------------|------|
| `api`      | 4096 |
| `frontend` | 4097 |

Adding a workspace with a name that sorts earlier shifts the ports of those that follow.

---

## Add multiple paths

Pass more than one directory to `paths` when an agent needs access to several repositories at once:

```toml
[workspaces.api]
paths = [
  "/home/you/projects/api",
  "/home/you/projects/shared-libs",
]
```

All listed paths are mounted read-write. The first is the working directory.

---

## Allow specific hosts or networks

By default, containers cannot reach private networks. To grant access to specific services, use `allowed_hosts` or `allowed_networks`.

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.10.5.0/24"]
```

See [How to allow specific hosts or networks](network-access.md) for step-by-step instructions.

---

## Set a custom image

You can point a workspace at a remote Dockerfile instead of using the default registry image:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/myproject.Dockerfile"
```

See [How to use a custom Docker image](custom-images.md) for all image customization options.

---

## Set a build context

When building a workspace-specific image layer, jailoc uses the parent directory of the workspace `dockerfile` as the Docker build context by default. Override it with `build_context`:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
build_context = "/home/you/projects/myproject/docker"
```

---

## Set a connection mode

Control how `jailoc attach` connects to the running container:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
mode = "remote"   # or "exec", or omit for auto-detect
```

See [How to switch between remote and exec mode](access-modes.md) for details.

---

## Set environment variables

Pass environment variables to the agent container using the `env` field:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
env = ["MY_TOKEN=abc123", "LOG_LEVEL=debug"]
```

To load variables from a file, use `env_file`. The file must exist at config load time and follow Docker `.env` format (KEY=VALUE, `#` comments, quoted values):

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
env_file = ["~/.config/jailoc/api.env"]
```

Both can be combined. `env` entries override `env_file` entries with the same key.

To apply env vars to all workspaces, use the `[defaults]` section:

```toml
[defaults]
env = ["GOPRIVATE=*.example.com"]
env_file = ["~/.config/jailoc/shared.env"]
```

!!! note
    Several keys are reserved and cannot be set: `OPENCODE_LOG`, `OPENCODE_SERVER_PASSWORD`, `DOCKER_HOST`, `DOCKER_TLS_CERTDIR`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`. Setting any of these causes a config validation error.
