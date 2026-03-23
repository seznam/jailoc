# How to switch between remote and exec mode

jailoc supports two ways to attach to a running workspace container. This guide shows how to configure them. For a full explanation of when to use each mode and the tradeoffs involved, see [Access modes](../explanation/access-modes.md).

---

## Let jailoc auto-detect (default)

By default, jailoc checks whether `opencode` is on your `PATH`:

- If found, it uses **remote** mode (`opencode attach`).
- If not found, it falls back to **exec** mode (`docker exec` into the container).

You don't need any config for this. Just run:

```bash
jailoc attach
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
jailoc attach --remote   # force remote mode for this run
jailoc attach --exec     # force exec mode for this run
```

The flag takes precedence over both the config value and auto-detection.
