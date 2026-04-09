# How to configure workspaces

This guide shows how to define and tune workspaces in jailoc's configuration file. For a full reference of every field and its type, see [Configuration reference](../reference/configuration.md).

## Config file location

jailoc stores its configuration at:

```
~/.config/jailoc/config.toml
```

The file is created automatically on first run with a `default` workspace already in place. You can edit it with any text editor.

---

## Automatic workspace detection

Most commands accept an explicit `--workspace` flag. When omitted, jailoc selects the workspace automatically using this resolution order:

1. **Explicit `--workspace` flag** — if set, use that workspace.
2. **Longest-prefix CWD match** — compare the current working directory against every workspace's configured `paths`. The workspace whose path is the longest matching prefix wins. Equal-length matches break alphabetically.
3. **`default` fallback** — if no path matches, the `default` workspace is used.

Given these two workspaces:

```toml
[workspaces.broad]
paths = ["/home/you/projects"]

[workspaces.specific]
paths = ["/home/you/projects/api"]
```

Running `jailoc up` from `/home/you/projects/api/src` selects `specific` (longer prefix match). Running from `/home/you/projects/other` selects `broad`.

`jailoc add` uses the path being added (not the raw CWD) for detection. If `--workspace` is set explicitly and the path being added is not under any of that workspace's configured paths, jailoc returns an error.

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

To use a pre-built image directly, set `image` in the workspace block:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
image = "myregistry.example.com/myteam/myimage:v1.2.3"
```

To build from a custom Dockerfile instead, use `dockerfile`:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
dockerfile = "https://git.example.com/team/dockerfiles/-/raw/main/myproject.Dockerfile"
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

Control how `jailoc` connects to the running container:

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
    Several keys are reserved and cannot be set: `OPENCODE_LOG`, `OPENCODE_SERVER_PASSWORD`, `DOCKER_HOST`, `DOCKER_TLS_CERTDIR`, `DOCKER_CERT_PATH`, `DOCKER_TLS_VERIFY`, `SSH_AUTH_SOCK`. Setting any of these causes a config validation error.

---

## Forward SSH agent and Git config

To let the agent clone private repositories or push over SSH, enable SSH agent forwarding. Git configuration is mounted by default.

```toml
[defaults]
ssh_auth_sock = true
```

These can also be set per-workspace. See [How to pass through SSH and Git config](ssh-git-passthrough.md) for details.

---

## Set resource limits

Control the CPU and memory allocated to the opencode container with `cpu` and `memory`:

```toml
[defaults]
cpu = 2.0
memory = "4g"

[workspaces.heavy-agent]
cpu = 8.0
memory = "16g"
```

Both fields are optional. Workspace values override defaults; when neither is set, the fallback is `2.0` CPU cores and `"4g"` memory. The `memory` field accepts Docker memory format: a positive integer optionally followed by `k`, `m`, or `g` (e.g. `512m`, `4g`, `1024`).
