# How to use the TUI sidebar plugin

The TUI sidebar plugin shows the workspace name in the OpenCode sidebar of a jailoc session.

## Setup

The plugin is bundled with jailoc and needs no configuration. At `jailoc up`, jailoc writes the plugin and a TUI config to its cache directory and shares them read-only into the VM. Every session starts with `OPENCODE_TUI_CONFIG` pointing at that config, so OpenCode loads the plugin automatically.

## Apply changes

The plugin files are written at `jailoc up`. After upgrading jailoc, restart the workspace:

```bash
jailoc restart <name>
```

## Troubleshooting

### Plugin not showing in sidebar

Check the environment inside a session:

```bash
env | grep -E 'JAILOC|OPENCODE_TUI_CONFIG'
```

You should see `JAILOC=1`, `JAILOC_WORKSPACE=<name>` and `OPENCODE_TUI_CONFIG=/etc/jailoc-cache/tui-container.json`. The plugin renders nothing without `JAILOC`.

### Plugin not loading

Check that the plugin directory is visible in the VM:

```bash
ls /etc/jailoc-tui-plugin/
```

If it's empty or missing, run `jailoc restart <name>`. See [Troubleshooting](troubleshooting.md) for other session problems.
