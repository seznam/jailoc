# Read-only agent configs: your instructions inside the VM, your host untouched

*November 2026 · release 2.4*

Up to jailoc 2.3, the agent you picked decided how much of your host setup came along. OpenCode mounted `~/.config/opencode` and `~/.opencode` (plus `~/.omo` for OpenCode 1) **read-write** from the host into every workspace. Claude Code, GitHub Copilot CLI and Antigravity CLI got nothing: no instructions, no skills, no commands, no settings.

Both halves were wrong. A read-write config mount meant a prompt-injected agent in one workspace could rewrite plugins, hooks, MCP servers or instructions, and the next OpenCode process on your host or in any other workspace would run them. Switching to `claude`, `copilot` or `agy` went the other way: you started without your `CLAUDE.md`, your custom commands and skills, your Copilot instructions and MCP config, or your `GEMINI.md`, unless you wired up `snapshots` by hand.

jailoc 2.4 adds `import_agent_config`. Every agent now gets a read-only copy of its host configuration, and none of them can write back.

**TL;DR**

- `import_agent_config` (default `true`) copies the selected agent's host configuration into the VM read-only at `jailoc up` and `jailoc reload`.
- OpenCode no longer mounts `~/.config/opencode` or `~/.opencode` read-write. Claude Code, Copilot CLI and Antigravity CLI now get your instructions, skills and settings.
- Logins and session history stay writable in the workspace's persistent state directory; only the copied files are read-only.
- Edit config on the host, run `jailoc reload`, and running workspaces see the new copy without a reboot.
- Each copy is a default snapshot, so you can drop one file, replace it, turn the whole thing off, or opt back into a read-write mount.

## What gets copied

With `import_agent_config = true`, jailoc copies these host paths to the same path under `/home/agent`:

| `agent` | Copied from the host |
|---|---|
| `opencode`, `opencode2` | `~/.config/opencode`, without `node_modules` |
| `opencode1` | `~/.config/opencode`, without `node_modules`; `~/.omo/omo.jsonc`, `~/.omo/omo.json` |
| `claude` | `~/.claude/CLAUDE.md`, `~/.claude/RTK.md`, `~/.claude/settings.json`, `~/.claude/agents`, `~/.claude/commands`, `~/.claude/hooks`, `~/.claude/skills`, `~/.claude/output-styles` |
| `copilot` | `~/.copilot/copilot-instructions.md`, `~/.copilot/agents`, `~/.copilot/hooks`, `~/.copilot/skills`, `~/.copilot/mcp-config.json` |
| `agy` | `~/.gemini/GEMINI.md` |
| `omo` | `~/.omo/omo.jsonc`, `~/.omo/omo.json` |

The OpenCode copy gets the `.gitignore` OpenCode would write itself, unless the host directory already has one. For Copilot, jailoc deliberately skips `config.json` and `settings.json`. That's where Copilot keeps logins and preferences, and where jailoc writes its status line, so they stay in the workspace.

Two mounts changed alongside:

- `~/.agents` is still mounted read-only at `/home/agent/.agents` for every agent.
- `~/.opencode` is no longer mounted at all.

OpenCode 1's `~/.omo` needed its own answer. The OmO (`oh-my-openagent`) plugin writes locks, journals and state there, so mounting the whole directory read-only would break it, while leaving it out entirely would drop your `~/.omo/omo.jsonc`. `~/.omo` is now a per-workspace persistent state directory (`opencode1-omo`), and `import_agent_config` snapshots `~/.omo/omo.jsonc` and `~/.omo/omo.json` read-only on top of it (just as it does for the `omo` harness). If you want the old shared read-write host directory back, mount it explicitly:

```toml
[defaults.agents.opencode1]
mounts = ["~/.omo:/home/agent/.omo:rw"]
```

## How the copies sit on top of persistent state

Claude Code keeps its state in `~/.claude`, Copilot in `~/.copilot`, Antigravity in `~/.gemini`. Those directories hold logins, session history and runtime files, and they live in the workspace's writable persistent state directory. Your config files live in the same directories, which is why a plain directory mount doesn't work here.

So jailoc splits the job. At `jailoc up` and `jailoc reload`, the host side stages the configured files into the read-only internal share. Inside the VM, `jailoc-guest` bind-mounts each staged file or directory read-only on top of the matching path in the state directory. The result:

- Logins and session history persist across restarts, exactly as before.
- Instructions, hooks, `settings.json` and skills are read-only in the VM and never reach the host.

Changed `CLAUDE.md` or something in `~/.config/opencode` on the host? Run:

```bash
jailoc reload
```

Running workspaces switch to the new snapshot generation without rebooting the VM or dropping sessions.

A few edge cases are handled for you:

- A host file that doesn't exist is skipped silently.
- If a destination is already covered by a share, for example an explicit mount of `~/.claude` or a named `profile` state directory, the default copy is skipped.
- When you later turn a copy off, the empty mount-point placeholders it left in the persistent state directory are removed on the next start. Agents won't trip over an empty `settings.json` or `mcp-config.json`.

## Customizing, opting out, or sharing read-write

Each copy is a default snapshot with the lowest priority, so the usual [`snapshots`](../reference/configuration.md#snapshots) rules apply.

Turn it off for one workspace, or for all of them in `[defaults]`:

```toml
[workspaces.scratch]
import_agent_config = false
```

Drop a single file and keep the rest with an empty-host snapshot:

```toml
[defaults]
snapshots = [":/home/agent/.claude/settings.json"]
```

Replace a copy with a different file or directory by giving a snapshot (or an agent mount) the same destination:

```toml
[workspaces.client]
snapshots = ["~/client/CLAUDE.md:/home/agent/.claude/CLAUDE.md"]

[defaults.agents.opencode-legacy]
from = "opencode"
mounts = ["~/.config/opencode-legacy:/home/agent/.config/opencode"]
```

Want the agent to edit your host config, the way OpenCode used to? Mount it read-write, and the mount replaces the copy:

```toml
[defaults.agents.opencode]
mounts = ["~/.config/opencode:/home/agent/.config/opencode:rw"]
```

!!! warning
    A read-write config mount crosses the trust boundary. Whatever the agent writes there runs on your host and in every other workspace that uses the same mount.

## Trade-offs to know about

**OpenCode local plugins.** `node_modules` isn't copied, because it can contain native binaries built for the host platform. OpenCode also skips `bun install` on a read-only config directory. Plugins listed in `plugin` still install from npm into `~/.cache`. A local file plugin that imports npm packages needs those packages in the workspace image.

**Copies shadow state directories.** While `~/.claude/skills` or `~/.copilot/skills` is copied from the host, it hides any skills the agent installs into that path inside the VM. If you want per-workspace mutable skills, drop that one copy:

```toml
[defaults]
snapshots = [":/home/agent/.claude/skills"]
```

**Symlinks stay symlinks.** Links inside copied directories are copied as links, not followed. A link pointing to a host path that isn't shared into the VM won't resolve.

Read more: [Coding Agents](../how-to/coding-agents.md#use-your-host-agent-configuration), [Threat Model](../explanation/threat-model.md), [Security Boundaries](../explanation/security-boundaries.md), [Configuration Reference](../reference/configuration.md#import_agent_config).
