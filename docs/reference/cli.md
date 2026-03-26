# CLI Reference

## Synopsis

```
jailoc [command] [flags]
```

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--workspace` | `-w` | `default` | Target workspace name. Overrides CWD-based auto-detection where applicable. |

---

## Commands

### `jailoc`

Auto-detect the workspace from the current working directory. If no matching workspace is found, prompts to add the current directory. If the workspace is not running, starts it. Attaches to the running environment.

```
jailoc [flags]
```

| Flag | Description |
|------|-------------|
| `--remote` | Force remote connection mode (runs `opencode attach` on the host). |
| `--exec` | Force exec connection mode (runs `docker exec` into the container). |

When neither `--remote` nor `--exec` is specified, the connection mode is determined by the workspace `mode` field in configuration, falling back to auto-detection. See the [access modes how-to](../how-to/access-modes.md) for configuration steps, or the [access modes explanation](../explanation/access-modes.md) for the difference between modes.

---

### `jailoc up`

Start the Docker Compose environment for the target workspace. No-op if the workspace containers are already running.

```
jailoc up [flags]
```

Resolves the container image (see [Image Resolution](image-resolution.md)), generates a `docker-compose.yml` in `~/.cache/jailoc/{workspace}/`, and starts two containers: `opencode` and `dind`.

---

### `jailoc down`

Stop and remove the containers for the target workspace.

```
jailoc down [flags]
```

Equivalent to `docker compose down` on the generated compose file. Does not remove named volumes or the image.

---

### `jailoc attach`

Attach to a running workspace environment from the host.

```
jailoc attach [flags]
```

| Flag | Description |
|------|-------------|
| `--remote` | Force remote connection mode (runs `opencode attach` on the host). |
| `--exec` | Force exec connection mode (runs `docker exec` into the container). |

The workspace must already be running. See [access modes explanation](../explanation/access-modes.md) for the difference between `--remote` and `--exec`.

If the `opencode` container stops or is replaced while attachment is active, `jailoc attach` exits instead of waiting indefinitely for the underlying client session to recover.

---

### `jailoc status`

Print the state and assigned port for each configured workspace.

```
jailoc status [flags]
```

Output lists all workspaces defined in configuration. For each workspace, shows the container state (running, stopped, or unknown) and the host port it is assigned.

---

### `jailoc logs`

Stream container logs from the target workspace environment.

```
jailoc logs [flags]
```

Streams combined stdout and stderr from the `opencode` and `dind` containers. Follows log output until interrupted.

---

### `jailoc config`

Print the current resolved configuration.

```
jailoc config [flags]
```

Reads `~/.config/jailoc/config.toml`, resolves all defaults and `~` expansions, and prints the result. Useful for verifying what values are in effect.

---

### `jailoc add`

Add the current working directory to the target workspace's `paths` list.

```
jailoc add [flags]
```

Appends the current directory to `workspaces.<name>.paths` in `~/.config/jailoc/config.toml`. The path must not be under a forbidden system prefix. See the [configuration reference](configuration.md) for path validation rules.
