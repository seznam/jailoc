# How to enable the TUI sidebar plugin

The TUI sidebar plugin displays the workspace name in the OpenCode sidebar when running inside a jailoc container. Use this guide with a `jailoc` v1.x release that includes the bundled TUI sidebar plugin and a workspace configured in `~/.config/jailoc/config.toml`.

---

## Automatic setup

The plugin is bundled with jailoc — no separate download is needed. When you run `jailoc`, jailoc sets `OPENCODE_TUI_CONFIG` to a generated plugin configuration when your host does not already provide a custom `~/.config/opencode/tui.json`.

If you don't have a custom `~/.config/opencode/tui.json` on the host, the plugin works with no additional configuration.

---

## Manual setup (custom tui.json)

If you have a custom `~/.config/opencode/tui.json`, jailoc does not override it. Add the shared plugin directory to your `tui.json` manually:

```json
{
  "plugin": ["file:///Users/<you>/.cache/jailoc/tui-plugin"]
}
```

The `tui-plugin` directory is shared across all workspaces — jailoc writes it once to `~/.cache/jailoc/tui-plugin/` during startup.

---

## Apply changes

Restart the workspace to apply the plugin configuration:

```bash
jailoc down <name> && jailoc up <name>
```

---

## Troubleshooting

### Plugin not showing in sidebar

Verify the `JAILOC` environment variable is set inside the container:

```bash
env | grep JAILOC
```

You should see `JAILOC=1` and `JAILOC_WORKSPACE=<name>`. The plugin renders nothing when `JAILOC` is absent.

### Plugin not loading

Verify jailoc generated the TUI config and plugin files:

```bash
ls ~/.cache/jailoc/tui.json ~/.cache/jailoc/tui-container.json ~/.cache/jailoc/tui-plugin/
```

If they are missing, restart any workspace with `jailoc down <name> && jailoc up <name>`.
