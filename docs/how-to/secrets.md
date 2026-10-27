# How to pass secrets to a workspace

This guide shows how to pass sensitive values, such as API keys, tokens or credential files, into a workspace VM. For how secrets are delivered and protected, see [Secrets explanation](../explanation/secrets.md).

jailoc reads every secret on the host at `jailoc up` and `jailoc reload` and sends it to the guest over SSH. Environment secrets reach sessions as environment variables only. File secrets land in `/run/secrets/<NAME>`, a tmpfs, with mode `0400` and owned by the `agent` user. jailoc never writes secret values to host disk.

---

## Pass an API key or token via environment variable

1. Set the variable on the host:

    ```bash
    export HOST_MCP_TOKEN="secret-token-value"
    ```

2. Add an environment secret to `~/.config/jailoc/config.toml`:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.env.MCP_SERVER_TOKEN]
    from_env = "HOST_MCP_TOKEN"
    ```

3. Start the workspace:

    ```bash
    jailoc up my-project
    ```

Sessions see the value as the `MCP_SERVER_TOKEN` environment variable.

---

## Pass a credential file

1. Add a file secret pointing to an absolute or `~` path on the host:

    ```toml
    [workspaces.my-project.secrets.file.db_cert]
    from_file = "~/.config/my-project/db.pem"
    ```

2. Start the workspace:

    ```bash
    jailoc up my-project
    ```

The contents land in `/run/secrets/db_cert`. File secrets aren't exported to the environment.

---

## Load a host file into an environment variable

Use `from_file` under `secrets.env.<NAME>`:

```toml
[workspaces.my-project.secrets.env.API_KEY]
from_file = "~/.secrets/api_key.txt"
```

Sessions get the file contents as `API_KEY`, with the trailing line break stripped. The value must fit on one line; use a file secret for multi-line content such as a PEM key.

---

## Read a secret from a password manager or keychain

Use `from_command` to run a command on the host and use its standard output as the value:

```toml
[workspaces.my-project.secrets.env.GH_TOKEN]
from_command = "gh auth token"

[workspaces.my-project.secrets.env.OPENAI_API_KEY]
from_command = "op read op://Private/OpenAI/credential"

[workspaces.my-project.secrets.file.npmrc]
from_command = "security find-generic-password -s npmrc -w"
```

jailoc runs each command with `/bin/sh -c` at `jailoc up` and `jailoc reload`, before it boots or changes the VM. The command gets your terminal, so a password manager can ask you to unlock it. A command that exits non-zero, prints nothing or runs longer than two minutes stops the operation.

---

## Set global default secrets

Declare secrets under `[defaults.secrets.env.<NAME>]` or `[defaults.secrets.file.<NAME>]` to give them to every workspace:

```toml
[defaults.secrets.env.API_TOKEN]
from_env = "GLOBAL_API_TOKEN"
```

A workspace secret with the same name replaces the default entry completely.

---

## Rotate or remove a secret

Edit the config or the host source, then push the change to the running VM:

```bash
jailoc reload my-project
```

jailoc reads every source again, rewrites `/run/secrets` and removes files of secrets that aren't configured any more. New sessions get the updated environment. Processes that read `/run/secrets/<NAME>` see the new value immediately.

!!! note
    Running sessions keep the environment they started with. Open a new session with `jailoc --new` to pick up changed environment secrets. See [Sessions](sessions.md).

!!! warning
    The agent can read every secret configured for its workspace. Configure only the secrets it needs, and prefer short-lived, narrowly scoped tokens.
