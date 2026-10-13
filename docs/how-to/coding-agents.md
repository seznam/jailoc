# How to choose a coding agent

Each workspace runs one coding agent: OpenCode by default, or Claude Code, GitHub Copilot CLI or OmO. jailoc installs the agent into the workspace image and starts it in unattended mode, because the VM is the security boundary.

## Select the agent

Set `agent` on the workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude"
```

Values: `opencode`, `opencode1`, `opencode2`, `claude`, `copilot`, `omo`. Set `agent` in `[defaults]` to change it for every workspace.

`opencode` is an alias of `opencode2`, OpenCode 2. `opencode1` runs OpenCode 1 (`opencode-ai`). The two keep their sessions apart: OpenCode 2 doesn't see the sessions of OpenCode 1, and switching back and forth doesn't mix their databases. Both read the same `~/.config/opencode`. To keep using OpenCode 1 and its existing sessions:

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

## Keep OpenCode from changing its configuration

The built-in OpenCode mount shares `~/.config/opencode` read-write with the host and every OpenCode workspace, so an agent can add plugins or instructions that run everywhere else. Mount it read-only:

```toml
[defaults.agents.opencode]
mounts = ["~/.config/opencode:/home/agent/.config/opencode:ro"]
```

OpenCode then can't install plugins inside the VM. Install them on the host, where `~/.config/opencode` is writable.

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
