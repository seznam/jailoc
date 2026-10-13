# How to enable the TUI sidebar plugin

The TUI sidebar plugin displays the workspace name in the OpenCode sidebar when running inside a jailoc container. Use this guide with OpenCode 2 and a workspace configured in `~/.config/jailoc/config.toml`.

---

## Automatic setup

The plugin is bundled with jailoc — no separate download is needed. jailoc writes it to `~/.cache/jailoc/tui-plugin/` during startup and loads it into the OpenCode terminal client through the `OPENCODE_CLI_CONFIG_CONTENT` environment variable:

- In **remote** mode, the host-side client loads the plugin from `~/.cache/jailoc/tui-plugin/`.
- In **exec** mode, the plugin directory is mounted read-only at `/etc/jailoc-tui-plugin` and the in-container client loads it from there.

Because inline configuration replaces the `plugins` array of `~/.config/opencode/cli.json`, jailoc copies your existing `plugins` entries into the inline configuration and appends its own. When `OPENCODE_CLI_CONFIG_CONTENT` is already set in your shell, jailoc extends that value instead and leaves `cli.json` out of the merge.

!!! note
    jailoc skips the plugin when it cannot merge safely: when `cli.json` is not plain JSON (for example, it contains comments), when `OPENCODE_CLI_CONFIG_CONTENT` is not valid JSON, or when only an OpenCode 1 `tui.json` exists. OpenCode migrates `tui.json` into `cli.json` on the first start of its terminal client; run `opencode` once on the host to complete the migration.

---

## Manual setup

To load the plugin when jailoc skips it, add the shared plugin directory to `~/.config/opencode/cli.json`:

```json
{
  "plugins": ["file:///Users/<you>/.cache/jailoc/tui-plugin"]
}
```

The `tui-plugin` directory is shared across all workspaces. The plugin renders nothing outside a jailoc session, so the entry is safe to keep for regular OpenCode use.

---

## Apply changes

Restart the workspace to rewrite the plugin files:

```bash
jailoc restart <name>
```

---

## Troubleshooting

### Plugin not showing in sidebar

The sidebar badge appears inside a session, not on the home screen. Verify the `JAILOC` environment variable is set inside the container:

```bash
env | grep JAILOC
```

You should see `JAILOC=1` and `JAILOC_WORKSPACE=<name>`. The plugin renders nothing when `JAILOC` is absent.

### Plugin not loading

Verify jailoc wrote the plugin files:

```bash
ls ~/.cache/jailoc/tui-plugin/
```

The directory must contain `package.json` and `tui.js`. If they are missing, restart any workspace with `jailoc restart <name>`.
