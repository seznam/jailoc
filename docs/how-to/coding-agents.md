# How to choose a coding agent

Each workspace runs one coding agent: OpenCode by default, or Claude Code, GitHub Copilot CLI, OmO or Antigravity CLI. jailoc installs the agent into the workspace image and starts it with its unattended-mode flags by default, because the VM is the security boundary. [`default_args`](#change-the-default-arguments) changes them.

## Select the agent

Set `agent` on the workspace, or in `[defaults]` for every workspace, then start the workspace and open a session:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude"
```

```bash
jailoc up my-project
jailoc -w my-project
```

Values: `opencode`, `opencode1`, `opencode2`, `claude`, `copilot`, `omo`, `agy`. To switch the agent of a running workspace, change `agent` and run `jailoc restart my-project`; sessions fail with a hint to restart until you do.

`opencode` is an alias of `opencode2`, OpenCode 2. `opencode1` runs OpenCode 1 (`opencode-ai`). The two keep their sessions apart: OpenCode 2 doesn't see the sessions of OpenCode 1, and switching back and forth doesn't mix their databases. Both get a read-only copy of the same `~/.config/opencode`. To keep using OpenCode 1 and its existing sessions:

```toml
[defaults]
agent = "opencode1"
```

## Run several agents on one project

Configure one workspace per agent with the same path. Each workspace boots its own VM; in that directory, pick the workspace with `-w`:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]

[workspaces.my-project-claude]
paths = ["~/projects/my-project"]
agent = "claude"
```

```bash
jailoc -w my-project-claude
```

## Log in

The agent's state directory (for example `~/.claude` for Claude Code) persists across restarts, so log in once inside a session. Alternatively, provide an API token as an [env secret](secrets.md), for example:

```toml
[workspaces.my-project-claude.secrets.env.ANTHROPIC_API_KEY]
from_command = "security find-generic-password -s anthropic -w"
```

- **GitHub Copilot CLI** (`copilot`) signs in with a device code in the terminal. With no keyring in the VM, jailoc sets `storeTokenPlaintext` in `~/.copilot/settings.json` so Copilot keeps the token in `~/.copilot/config.json`. You can also pass `COPILOT_GITHUB_TOKEN` (or `GH_TOKEN`) as an env secret.
- **Antigravity CLI** (`agy`) prints a sign-in URL and reads the code back in the terminal, so the login works without a browser in the VM. With no keyring in the VM, it keeps the token in a file in `~/.gemini`, its state directory.

`jailoc down --purge` deletes the state, including logins (not a named profile, below).

## Share or discard agent state

By default each workspace keeps its own agent state. To reuse logins and sessions across workspaces, name a [profile](../reference/configuration.md#agent-profiles); to start clean every time, set `profile = false`:

```toml
[defaults.agents.claude]
profile = "work"

[workspaces.scratch.agents.claude]
profile = false
```

A named profile lives in `~/.local/share/jailoc/profiles/<name>`, survives `jailoc down --purge`, and is used by one running workspace at a time: stop the other workspace before starting one with the same profile. A custom agent does not inherit `profile` from the agent it is derived from.

!!! warning
    A profile carries the harness's hooks, plugins and MCP server settings along with the login, so an agent in one workspace can change what runs in every other workspace on the same profile. Share a profile only between workspaces you trust equally.

## Mount agent configuration

[`agents.<name>.mounts`](../reference/configuration.md#agent-mounts) adds mounts only while that agent is selected, for example a separate OpenCode configuration for a custom agent. An entry with an empty host, such as `":/home/agent/.agents"`, removes a built-in mount:

```toml
[defaults.agents.opencode-legacy]
from = "opencode"
mounts = ["~/.config/opencode-legacy:/home/agent/.config/opencode"]
```

## Use your host agent configuration

jailoc copies the agent's configuration from the host into the VM read-only when the VM starts and on `jailoc reload`, so an agent in the VM can't change what runs on the host or in other workspaces. [`import_agent_config`](../reference/configuration.md#import_agent_config) lists the files copied for each agent. Change the configuration on the host, then run `jailoc reload <name>`.

OpenCode doesn't install plugins into the read-only copy, and `node_modules` isn't copied. Plugins listed in `plugin` install into `~/.cache`; install the packages a local plugin imports in the image.

Leave out one copy with an empty-host snapshot, or all of them with `import_agent_config`:

```toml
[defaults]
snapshots = [":/home/agent/.claude/settings.json"]

[workspaces.scratch]
import_agent_config = false
```

To let the agent change the host configuration, mount it read-write; the mount replaces the copy:

```toml
[defaults.agents.opencode]
mounts = ["~/.config/opencode:/home/agent/.config/opencode:rw"]
```

!!! warning
    Every workspace using the mount, and the host, then run what the agent writes there.

## Use agent configuration from a dotfiles repository

To give the agent instructions, settings or plugins kept in a dotfiles checkout instead of its usual configuration directory, mount the directory read-only and point the agent at it with `args`. For Claude Code:

```toml
[defaults]
snapshots = ["~/dotfiles/claude/settings.json:/home/agent/.claude/settings.json"]

[defaults.agents.claude]
mounts = ["~/dotfiles/claude:/home/agent/.claude-dotfiles:ro"]
args = [
  "--append-system-prompt-file", "/home/agent/.claude-dotfiles/system-prompt.md",
  "--plugin-dir", "/home/agent/.claude-dotfiles/plugin",
]
```

The mount is live: edits on the host apply to the next session without `jailoc reload`. A missing host directory of a read-only mount fails `jailoc up`, so a wrong path is reported instead of sharing an empty directory.

!!! warning
    Don't pass `--settings` in `args`: jailoc already passes `--settings` with its own file (the workspace indicator), and a second `--settings` replaces it instead of merging with it. Snapshot the settings file to `/home/agent/.claude/settings.json` instead, as above. The snapshot replaces the copy of your host `~/.claude/settings.json` that `import_agent_config` makes, and a change to the file needs `jailoc reload`.

A symlink inside a path or mount that points outside every share or snapshot is dangling in the VM; `jailoc up` warns about it. Snapshot its target at the path the link names, or mount the directory that contains the target at the matching directory in the VM; mounts share directories only.

## Pass extra arguments

`args` is appended to every session command line:

```toml
[workspaces.my-project-claude.agents.claude]
args = ["--model", "sonnet"]
```

## Change the default arguments

Each agent starts with built-in `default_args`, such as `--dangerously-skip-permissions` for Claude Code. Set `default_args` to replace them, or `default_args = []` to start the agent without them. To keep Claude Code's own permission prompts and start in plan mode:

```toml
[workspaces.my-project-claude.agents.claude]
default_args = []
args = ["--permission-mode", "plan"]
```

To replace the defaults with another list, for example to start Claude Code in plan mode everywhere without needing `args`:

```toml
[defaults.agents.claude]
default_args = ["--permission-mode", "plan"]
```

Set it in `[workspaces.<ws>.agents.<name>]` for one workspace, or in `[defaults.agents.<name>]` for all. `opencode` and `opencode2` are separate tables. Arguments of `args` follow `default_args` on the command line, and each list is inherited on its own.

`default_args = []` and `args = []` differ: `default_args = []` removes the built-in defaults, while `args = []` only clears extra arguments and leaves the built-in defaults in place.

New, resumed and continued sessions use the current configuration. A session that's already running is unchanged. No `jailoc restart` is needed.

!!! warning
    `default_args` is not an access control. Imported agent settings or explicit `args` can still enable a permission bypass. The VM stays the security boundary.

## Use another version

jailoc pins the version of each agent. To install another one, set `version`:

```toml
[defaults.agents.claude]
version = "2.1.400"
```

The next `jailoc up` or `jailoc restart` builds the agent layer with that version. Use an exact version: the layer is rebuilt only when the version changes, so `latest` is rejected.

Antigravity CLI is installed from its GitHub release rather than npm. For a version other than the pinned one, jailoc reads the asset checksums from the GitHub API on every `jailoc up` and `jailoc restart`, and the download is checked against them.

## Define a custom agent

A [custom agent](../reference/configuration.md#custom-agents) is a built-in agent under another name with its own settings, for example a model preset or a version you want to try next to the pinned one. Settings it doesn't set come from the agent it is derived from: here `claude-opus` installs the same Claude Code version as `claude`.

```toml
[defaults.agents.claude-opus]
from = "claude"
args = ["--model", "opus"]

[workspaces.my-project-opus]
paths = ["~/projects/my-project"]
agent = "claude-opus"
```

## Install the agent yourself

To provide the agent from your own overlay Dockerfile, for example a build of your own, turn off the built-in installation. The binary (`claude`) must be on `PATH` in the image; an image used directly with `image` never gets the agent installed by jailoc and must contain it.

```toml
[workspaces.my-project-claude]
paths = ["~/projects/my-project"]
agent = "claude"
dockerfile = "~/projects/my-project/agent.Dockerfile"

[workspaces.my-project-claude.agents.claude]
install = false
```
