# 💻 CLI Reference

## 🔧 Commands

Use `--workspace` / `-w` to target a specific workspace (default: `default`).

| Command | Description |
|---------|-------------|
| `jailoc` | Auto-detect workspace from CWD, prompt to add if missing, start if not running, then attach. |
| `jailoc up` | Start the Docker Compose environment for the workspace. No-op if already running. |
| `jailoc down` | Stop and remove the containers for the workspace. |
| `jailoc attach` | Attach to a running workspace using `opencode attach` on the host. |
| `jailoc status` | Show running status and port for each configured workspace. |
| `jailoc logs` | Stream container logs from the workspace environment. |
| `jailoc config` | Print the current resolved config. |
| `jailoc add` | Add the current directory to a workspace's paths. |

## 🔌 Access Modes

jailoc supports two modes for connecting to the OpenCode server inside the container:

- **remote** (default when `opencode` is installed): Runs `opencode attach` on the host, connecting over the exposed port.
- **exec**: Runs `docker exec` into the container and launches `opencode` TUI directly inside.

Auto-detect selects `remote` if `opencode` is found on your PATH, otherwise falls back to `exec`.

Set in config for a permanent default:

```toml
# mode = ""        # auto-detect (default)
# mode = "remote"  # always use host opencode attach
# mode = "exec"    # always use docker exec
```

Or override per-run with flags:

```bash
jailoc              # auto-detect
jailoc --remote     # force remote mode
jailoc --exec       # force exec mode
```
