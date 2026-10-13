# How to migrate from jailoc v1

jailoc v2 runs every workspace in its own micro-VM. The Docker Compose backend of v1, remote mode and the published OpenCode port are gone. This guide moves an existing v1 configuration and its OpenCode data to v2.

## Recognize a v1 configuration

jailoc v2 refuses to load a config that contains v1 settings and names the setting:

```text
mode is a jailoc v1 setting; run `jailoc migrate` to update the config
```

These settings trigger it, in `[defaults]` or in any workspace:

| Setting | Why it's rejected |
|---------|-------------------|
| `backend = "docker"` | Workspaces run only as VMs. Omit `backend` or set `"vm"`. |
| `mode` (top-level) | Sessions always run over SSH into the VM; there's no remote or exec mode. |
| `password_mode` | No OpenCode server port is published, so there's no server password. |
| `expose_port` | Same reason. Use `ports` to publish services from the VM. |

In v1 an unset `backend` meant Docker, so `jailoc migrate` treats every workspace without `backend = "vm"` as a Docker workspace.

## Check the prerequisites

- A supported host: macOS on Apple Silicon, or Linux on amd64 with read and write access to `/dev/kvm`. See [Installation](installation.md).
- Docker Engine running on the host. `jailoc migrate` needs it to stop v1 workspaces and copy their data volumes.

## Preview the migration

```bash
jailoc migrate --dry-run
```

This prints the planned changes and the resulting config without touching anything.

## Run the migration

```bash
jailoc migrate
```

For each Docker workspace, jailoc:

1. Asks how to carry over every entry in `mounts`:
    - `mount`: keep it as a live directory share (offered for directories only).
    - `snapshot`: turn it into a read-only entry in `snapshots`. Single-file mounts must become snapshots, because the VM shares directories only.
    - `drop`: remove it.
2. Asks to confirm removing the v1 settings listed above.
3. Stops the running v1 workspace.
4. Copies its `opencode-data` and `opencode-cache` Docker volumes into the VM's persistent home directory, so sessions and logins carry over to OpenCode 1. The default agent is OpenCode 2, which keeps its sessions separately; set `agent = "opencode1"` to keep working with them (see [Choose a coding agent](coding-agents.md#select-the-agent)).
5. Sets `backend = "vm"`.

The previous config is kept as `config.toml.pre-vm-migration` next to `config.toml` (a timestamp is appended if that file already exists). Comments in the old file aren't carried over.

To accept the default answer to every question, for example in a script:

```bash
jailoc migrate -y
```

## Start the migrated workspace

```bash
jailoc up my-project
jailoc
```

The first start builds the image and boots the VM; later starts reuse the build cache and take a few seconds.

## Adjust your habits

| v1 | v2 |
|----|----|
| `jailoc attach` | `jailoc` or `jailoc --attach <ID>`. See [Sessions](sessions.md). |
| `jailoc --remote`, `jailoc --exec`, `--dir` | `jailoc [path]` opens the session in that directory. |
| `opencode attach` from the host to the published port | Not available. Use `jailoc` sessions; publish other services with `ports`. |
| Single-file `mounts` | `snapshots`. See [Workspace configuration](workspace-configuration.md). |
| `git_config` live mount | Read-only snapshot taken at VM start. See [SSH and Git passthrough](ssh-git-passthrough.md). |
| Secret files bind-mounted from the host | Pushed over SSH to `/run/secrets`. See [Secrets](secrets.md). |

After migrating, Docker is needed on the host only for building workspace images, and not even for that with `isolated_build`. See [Custom images](custom-images.md).

If something fails, see [Troubleshooting](troubleshooting.md). For all `migrate` flags, see the [CLI reference](../reference/cli.md).
