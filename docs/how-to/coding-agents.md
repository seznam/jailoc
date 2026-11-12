# How to choose a coding agent

Each workspace runs one coding agent: OpenCode by default, or Claude Code, GitHub Copilot CLI, OmO or Antigravity CLI. jailoc installs the agent into the workspace image and starts it in unattended mode, because the VM is the security boundary.

## Select the agent

Set `agent` on the workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude"
```

Values: `opencode`, `opencode1`, `opencode2`, `claude`, `copilot`, `omo`, `agy`. Set `agent` in `[defaults]` to change it for every workspace.

`opencode` is an alias of `opencode2`, OpenCode 2. `opencode1` runs OpenCode 1 (`opencode-ai`). The two keep their sessions apart: OpenCode 2 doesn't see the sessions of OpenCode 1, and switching back and forth doesn't mix their databases. Both get a read-only copy of the same `~/.config/opencode`. To keep using OpenCode 1 and its existing sessions:

```toml
[defaults]
agent = "opencode1"
```

Start the workspace and open a session:

```bash
jailoc up my-project
jailoc -w my-project
```

To switch the agent of a running workspace, change `agent` and run:

```bash
jailoc restart my-project
```

Sessions fail with a hint to restart until you do.

## Run several agents on one project

Configure one workspace per agent with the same path:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]

[workspaces.my-project-claude]
paths = ["~/projects/my-project"]
agent = "claude"
```

Each workspace boots its own VM. In that directory, pick the workspace with `-w`:

```bash
jailoc -w my-project-claude
```

## Log in

The agent's state directory (for example `~/.claude` for Claude Code) persists across restarts, so log in once inside a session. Alternatively, provide an API token as an [env secret](secrets.md), for example:

```toml
[workspaces.my-project-claude.secrets.env.ANTHROPIC_API_KEY]
from_command = "security find-generic-password -s anthropic -w"
```

Antigravity CLI (`agy`) prints a sign-in URL and reads the code back in the terminal, so the login works over the session without a browser in the VM. With no keyring in the VM, it keeps the token in a file in `~/.gemini`, its state directory.

`jailoc down --purge` deletes the state, including logins.

## Share or discard agent state

By default each workspace keeps its own agent state. To reuse logins and sessions across workspaces, name a profile; to start clean every time, set `profile = false`:

```toml
[defaults.agents.claude]
profile = "work"

[workspaces.scratch.agents.claude]
profile = false
```

A named profile lives in `~/.local/share/jailoc/profiles/<name>`, survives `jailoc down --purge`, and is used by one running workspace at a time: stop the other workspace before starting one with the same profile. A custom agent does not inherit `profile` from the agent it is derived from. See [Agent profiles](../reference/configuration.md#agent-profiles).

!!! warning
    A profile carries the harness's hooks, plugins and MCP server settings along with the login, so an agent in one workspace can change what runs in every other workspace on the same profile. Share a profile only between workspaces you trust equally.

## Mount agent configuration

`agents.<name>.mounts` adds mounts only while that agent is selected, for example a separate OpenCode configuration for a custom agent:

```toml
[defaults.agents.opencode-legacy]
from = "opencode"
mounts = ["~/.config/opencode-legacy:/home/agent/.config/opencode"]
```

An entry with an empty host, such as `":/home/agent/.agents"`, removes a built-in mount. See [Agent mounts](../reference/configuration.md#agent-mounts).

## Use your host agent configuration

jailoc copies the agent's configuration from the host into the VM read-only when the VM starts and on `jailoc reload`: `~/.config/opencode` for OpenCode, `CLAUDE.md`, `settings.json`, agents, commands, skills and output styles from `~/.claude` for Claude Code, instructions, agents, skills and MCP servers from `~/.copilot` for GitHub Copilot CLI, and `~/.gemini/GEMINI.md` for Antigravity CLI. An agent in the VM can't change what runs on the host or in other workspaces. Change the configuration on the host, then run `jailoc reload <name>`.

OpenCode doesn't install plugins into the read-only copy, and `node_modules` isn't copied. Plugins listed in `plugin` install into `~/.cache`; install the packages a local plugin imports in the image.

Leave out one copy with an empty-host snapshot, or all of them with `import_agent_config`:

```toml
[defaults]
snapshots = [":/home/agent/.claude/settings.json"]

[workspaces.scratch]
import_agent_config = false
```

To let the agent change the host configuration, mount it read-write; the mount replaces the copy. Every workspace using the mount, and the host, then run what the agent writes there:

```toml
[defaults.agents.opencode]
mounts = ["~/.config/opencode:/home/agent/.config/opencode:rw"]
```

See [`import_agent_config`](../reference/configuration.md#import_agent_config).

## Pass extra arguments

`args` is appended to every session command line:

```toml
[workspaces.my-project-claude.agents.claude]
args = ["--model", "sonnet"]
```

## Use another version

jailoc pins the version of each agent. To install another one, set `version`:

```toml
[defaults.agents.claude]
version = "2.1.400"
```

The next `jailoc up` or `jailoc restart` builds the agent layer with that version. Use an exact version: the layer is rebuilt only when the version changes, so `latest` is rejected.

Antigravity CLI is installed from its GitHub release rather than npm. For a version other than the pinned one, jailoc reads the asset checksums from the GitHub API on every `jailoc up` and `jailoc restart`, and the download is checked against them.

## Define a custom agent

A custom agent is a built-in agent under another name with its own settings, for example a model preset or a version you want to try next to the pinned one:

```toml
[defaults.agents.claude-opus]
from = "claude"
args = ["--model", "opus"]

[workspaces.my-project-opus]
paths = ["~/projects/my-project"]
agent = "claude-opus"
```

Settings not set on the custom agent come from the agent it is derived from: here `claude-opus` installs the same Claude Code version as `claude`. See [Custom agents](../reference/configuration.md#custom-agents).

## Install the agent yourself

To provide the agent from your own overlay Dockerfile, for example a build of your own, turn off the built-in installation:

```toml
[workspaces.my-project-claude]
paths = ["~/projects/my-project"]
agent = "claude"
dockerfile = "~/projects/my-project/agent.Dockerfile"

[workspaces.my-project-claude.agents.claude]
install = false
```

The binary (`claude`) must be on `PATH` in the image. An image used directly with `image` never gets the agent installed by jailoc and must contain it.

For the session commands, persistent directories and environment of each agent, see [Coding agents](../reference/configuration.md#coding-agents).
