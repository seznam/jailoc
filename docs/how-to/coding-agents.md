# How to choose a coding agent

Each workspace runs one coding agent: OpenCode by default, or Claude Code, GitHub Copilot CLI or OmO. jailoc installs the agent into the workspace image and starts it in unattended mode, because the VM is the security boundary.

## Select the agent

Set `agent` on the workspace:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude"
```

Values: `opencode`, `claude`, `copilot`, `omo`. Set `agent` in `[defaults]` to change it for every workspace.

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

## Pass extra arguments

`args` is appended to every session command line:

```toml
[workspaces.my-project-claude.agents.claude]
args = ["--model", "sonnet"]
```

## Install the agent yourself

To provide the agent from your own overlay Dockerfile, for example another version, turn off the built-in installation:

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
