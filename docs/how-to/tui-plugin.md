# How to enable the TUI sidebar plugin

The TUI sidebar plugin displays the workspace name in the OpenCode sidebar when running inside a jailoc container. Use this guide with a `jailoc` v1.x release that includes the bundled TUI sidebar plugin and a workspace configured in `~/.config/jailoc/config.toml`.

---

## Automatic setup

The plugin is bundled with jailoc — no separate download is needed. When you run `jailoc`, jailoc sets `OPENCODE_TUI_CONFIG` to a generated plugin configuration when your host does not already provide a custom `~/.config/opencode/tui.json`.

If you don't have a custom `~/.config/opencode/tui.json` on the host, the plugin works with no additional configuration.

---

## Manual setup (custom tui.json)

If you have a custom `~/.config/opencode/tui.json`, jailoc does not override it. Add the generated plugin directory to your `tui.json` manually:

```json
{
  "plugin": ["file:///Users/<you>/.cache/jailoc/<workspace>/tui-plugin"]
}
```

Replace `<workspace>` with your workspace name. The generated `tui-plugin` directory contains the embedded sidebar plugin that jailoc writes during startup.

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

Verify jailoc generated the TUI config files for the workspace:

```bash
ls ~/.cache/jailoc/<name>
```

You should see `tui.json`, `tui-container.json`, and `tui-plugin/`. If they are missing, restart the workspace with `jailoc down <name> && jailoc up <name>`.
