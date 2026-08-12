# How to pass secrets to a workspace

This guide shows how to pass sensitive values—such as API keys, tokens, or credential files—into a jailoc workspace container using secrets. For a deep dive into how secret mounting and permissions work, see [Secrets explanation](../explanation/secrets.md).

---

## Pass an API key or token via environment variable

To pass a secret from a host environment variable into the container, define a secret using `env` and `expose_env`.

1. Set the environment variable on your host system:

    ```bash
    export HOST_MCP_TOKEN="secret-token-value"
    ```

2. Add the secret to your workspace configuration in `~/.config/jailoc/config.toml`:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.mcp_token]
    env = "HOST_MCP_TOKEN"
    expose_env = "MCP_SERVER_TOKEN"
    ```

3. Start or restart the workspace:

    ```bash
    jailoc up my-project
    ```

Inside the container, the value is available as the `MCP_SERVER_TOKEN` environment variable and as a file at `/run/secrets/mcp_token`.

---

## Mount a credential file

To mount a file from your host into the container without exposing it as an environment variable, use `file`.

1. Add the secret definition pointing to a file on your host:

    ```toml
    [workspaces.my-project]
    paths = ["~/projects/my-project"]

    [workspaces.my-project.secrets.db_cert]
    file = "~/.config/my-project/db.pem"
    ```

2. Start the workspace:

    ```bash
    jailoc up my-project
    ```

The file is mounted read-only at `/run/secrets/db_cert` inside the container.

!!! note
    Files mounted with `file` must be world-readable (`o+r`) on the host unless `expose_env` is set. If your host file has restricted permissions (such as `0600`), add `expose_env` as described below.

---

## Pass a restricted host file into an environment variable

If you have a restricted credential file on your host (for example, a `0600` permission file) that you want to expose as an environment variable inside the container:

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]

[workspaces.my-project.secrets.api_key_file]
file = "~/.secrets/api_key.txt"
expose_env = "API_KEY"
```

When `expose_env` is set, jailoc's root entrypoint reads the file before dropping privileges to the unprivileged container user (UID 1000) and re-exports its contents into the `API_KEY` environment variable.

---

## Set global default secrets

To make a secret available across all workspaces, declare it under `[defaults.secrets.<NAME>]`:

```toml
[defaults.secrets.global_token]
env = "GLOBAL_API_TOKEN"
expose_env = "API_TOKEN"

[workspaces.my-project]
paths = ["~/projects/my-project"]
```

Workspace-level secret declarations with the same secret name completely replace the default entry for that secret name.
