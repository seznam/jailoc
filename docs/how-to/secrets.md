# How to pass secrets to a workspace

This guide shows how to pass sensitive values, such as API keys, tokens, or credential files, into a jailoc workspace container using secrets. For a deep dive into how secret mounting and permissions work, see [Secrets explanation](../explanation/secrets.md).

---

## Pass an API key or token via environment variable

To pass a secret from a host environment variable into the container, define an environment secret under `secrets.env.<NAME>`.

1. Set the environment variable on your host system:

    ```bash
    export HOST_MCP_TOKEN="secret-token-value"
    ```

2. Add the secret to your workspace configuration in `~/.config/jailoc/config.toml`:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.env.MCP_SERVER_TOKEN]
    from_env = "HOST_MCP_TOKEN"
    ```

3. Start or restart the workspace:

    ```bash
    jailoc up my-project
    ```

Inside the container, the value is available as the `MCP_SERVER_TOKEN` environment variable and as a file at `/run/secrets/MCP_SERVER_TOKEN`.

---

## Mount a credential file

To mount a credential file from your host into the container as a secret file, define a file secret under `secrets.file.<NAME>`.

1. Add the secret definition pointing to an absolute or tilde path on your host:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.file.db_cert]
    from_file = "~/.config/my-project/db.pem"
    ```

2. Start the workspace:

    ```bash
    jailoc up my-project
    ```

The file is mounted read-only at `/run/secrets/db_cert` inside the container. File secrets are not exported to environment variables.

---

## Pass a host credential file into an environment variable

File secrets defined under `secrets.file` do not export environment variables. To expose the contents of a host credential file as a container environment variable:

1. Read the file into an environment variable on your host system:

    ```bash
    export HOST_API_KEY="$(cat ~/.secrets/api_key.txt)"
    ```

2. Reference the host environment variable using `secrets.env`:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.env.API_KEY]
    from_env = "HOST_API_KEY"
    ```

3. Start the workspace:

    ```bash
    jailoc up my-project
    ```

Inside the container, the value is available as the `API_KEY` environment variable.

---

## Set global default secrets

To make a secret available across all workspaces, declare it under `[defaults.secrets.env.<NAME>]` or `[defaults.secrets.file.<NAME>]`:

```toml
[defaults.secrets.env.API_TOKEN]
from_env = "GLOBAL_API_TOKEN"

[workspaces.my-project]
paths = ["~/projects/my-project"]
```

Workspace-level secret declarations with the same secret name completely replace the default entry for that name.
