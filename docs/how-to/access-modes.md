# How to switch between remote and exec mode

jailoc supports two ways to attach to a running workspace container. This guide shows how to configure them. For a full explanation of when to use each mode and the tradeoffs involved, see [Access modes](../explanation/access-modes.md).

---

## Let jailoc auto-detect (default)

By default, jailoc checks whether `opencode` or `opencode-cli` is on your `PATH`:

- If found, it uses **remote** mode (runs whichever binary was found with `attach`).
- If neither is found, it falls back to **exec** mode (`docker exec` into the container).

You don't need any config for this. Just run:

```bash
jailoc
```

---

## Set a fixed mode in config

To pin a workspace to a specific mode regardless of what's on your `PATH`, set `mode` in the workspace config:

```toml
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
mode = "remote"
```

Valid values:

| Value      | Behavior                                      |
|------------|-----------------------------------------------|
| `""`       | Auto-detect (default, same as omitting the field) |
| `"remote"` | Always use `opencode attach` on the host      |
| `"exec"`   | Always use `docker exec` into the container   |

---

## Override the mode for a single run

Use CLI flags to force a mode without changing your config:

```bash
jailoc --remote   # force remote mode for this run
jailoc --exec     # force exec mode for this run
```

The flag takes precedence over both the config value and auto-detection.

---

## Open a specific directory

By default, `opencode attach` opens the workspace root. Use `--dir` to open a subdirectory instead:

```bash
jailoc --dir /home/you/projects/myproject/src
```

The root `jailoc` command does this automatically when run from a subdirectory — it resolves the current working directory and forwards it as `--dir`:

```bash
cd ~/projects/myproject/src
jailoc    # attaches with --dir pointing at src/
```

Because workspace paths are identity-mounted (the host path and the container path are the same), the absolute host path works inside the container as-is.

---

## Understand attach behavior during rebuilds or restarts

Both modes fail fast if the `opencode` container stops or is replaced while you are attached.

- In **remote** mode, jailoc terminates the host-side `opencode attach` process instead of leaving it blocked against a dead container.
- In **exec** mode, jailoc cancels the `docker exec` session so your terminal is restored instead of staying stuck in a hung interactive session.

This is most noticeable when you rebuild, bake, or otherwise replace the workspace container while an attach session is active. After the attach command exits, run `jailoc` again to reconnect to the new container.
