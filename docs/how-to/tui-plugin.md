# How to use the workspace indicator

The workspace indicator shows `🔒 jailoc / <name>` in the agent's interface, so you can tell a jailoc session from one on the host. Every built-in agent shows it:

| Agent | Where it shows | How jailoc loads it |
|-------|----------------|---------------------|
| OpenCode 1 (`opencode1`) | Sidebar | TUI plugin listed in the TUI config that `OPENCODE_TUI_CONFIG` points at |
| OpenCode 2 (`opencode`, `opencode2`) | Sidebar, inside a session | TUI plugin listed in `OPENCODE_CLI_CONFIG_CONTENT` |
| Claude Code (`claude`) | Status line | `--settings /etc/jailoc-tui-plugin/claude-settings.json` |
| GitHub Copilot CLI (`copilot`) | Status line | `statusLine` in `~/.copilot/settings.json` |
| OmO (`omo`) | Footer status | `--extension /etc/jailoc-tui-plugin/omo.js` |

## Load the indicator

The indicator is bundled with jailoc and needs no configuration. At `jailoc up`, jailoc writes the agent's indicator files to its cache directory and shares them read-only into the VM at `/etc/jailoc-tui-plugin`.

- OpenCode 2: `OPENCODE_CLI_CONFIG_CONTENT` lists the plugins of `cli.json` in your OpenCode config directory (or of `tui.json` when there's no `cli.json`) followed by the jailoc plugin. The list replaces the plugins of `cli.json`; its other settings still apply. jailoc reads the file when the VM starts, so run `jailoc restart <name>` after changing the plugins. If the file can't be parsed, jailoc warns and doesn't load its plugin, leaving your configuration as it is.
- Claude Code: the `--settings` file replaces the status line of your settings. The jailoc status line runs the `statusLine` command of your settings (`.claude/settings.local.json` or `.claude/settings.json` in the project, then `settings.json` in the Claude config directory) and puts the workspace before its first line.
- GitHub Copilot CLI: Copilot reads a status line only from its user settings, so jailoc adds one to `settings.json` in the agent's persistent state directory when the file sets none. If you set your own `statusLine` there, jailoc leaves it alone and Copilot shows yours instead. jailoc doesn't touch the file when you mount `~/.copilot` from the host.

## Update the indicator

The indicator files are written at `jailoc up`. After upgrading jailoc, restart the workspace:

```bash
jailoc restart <name>
```

## Fix a missing indicator

### The agent doesn't show the workspace

Check the environment inside a session:

```bash
env | grep -E 'JAILOC|OPENCODE_TUI_CONFIG|OPENCODE_CLI_CONFIG_CONTENT'
```

You should see `JAILOC=1` and `JAILOC_WORKSPACE=<name>`; the OpenCode plugins and the OmO extension render nothing without `JAILOC`. For OpenCode 1 you should also see `OPENCODE_TUI_CONFIG=/etc/jailoc-cache/tui-container.json`, and for OpenCode 2 `OPENCODE_CLI_CONFIG_CONTENT` with `file:///etc/jailoc-tui-plugin` among its `plugins`. OpenCode 2 shows the sidebar inside a session, not on the home screen. Claude Code shows no status line until you trust the workspace folder, or when your settings set `disableAllHooks`.

### The indicator files are missing

Check that the indicator directory is visible in the VM:

```bash
ls /etc/jailoc-tui-plugin/
```

If it's empty or missing, run `jailoc restart <name>`. See [Troubleshooting](troubleshooting.md) for other session problems.
