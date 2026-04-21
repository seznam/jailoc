# How to enable the TUI sidebar plugin

The TUI sidebar plugin displays the workspace name in the OpenCode sidebar when running inside a jailoc container.

## Prerequisites

- jailoc v0.7.0 or later
- A workspace configured in `~/.config/jailoc/config.toml`

---

## Automatic setup

The plugin is bundled with jailoc — no separate download is needed. When you run `jailoc attach`, jailoc sets the `OPENCODE_TUI_CONFIG` environment variable pointing to the plugin configuration.

If you don't have a custom `~/.config/opencode/tui.json` on the host, the plugin works with no additional configuration.

---

## Manual setup (custom tui.json)

If you have a custom `~/.config/opencode/tui.json`, jailoc does not override it. Add the plugin entry to your `tui.json` manually:

```json
{
  "plugin": ["https://github.com/seznam/jailoc/releases/download/v<version>/seznam-jailoc-<version>.tgz"]
}
```

Replace `<version>` with the jailoc version you are using (e.g. `0.7.0`).

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

Verify `tui.json` is mounted inside the container:

```bash
cat /etc/jailoc/tui.json
```

If the file is missing, check that the workspace is running with jailoc v0.7.0 or later.
