# Secrets Architecture

jailoc provides a secret management system for passing sensitive credentials, such as API keys, tokens, or certificate files, into workspace containers. It builds on Docker Compose's native secrets specification while managing environment variable injection and container file permissions.

For step-by-step setup instructions, see [How-to: Secrets](../how-to/secrets.md).

---

## Output Identity and Secret Categories

Every secret is defined under a specific category in the workspace configuration: `secrets.env.<NAME>` for environment variables, or `secrets.file.<NAME>` for files.

The section name `<NAME>` determines the secret's output identity inside the container:

- For environment secrets, `<NAME>` is the name of the environment variable exported inside the container.
- For file secrets, `<NAME>` is the file basename under `/run/secrets/<NAME>`.

---

## The Two Secret Mechanisms

Docker Compose handles environment secrets and file secrets through distinct container mechanisms.

### 1. Environment Secrets (`secrets.env.<NAME>`)

Environment secrets map a host environment variable into a container environment variable. A secret configured under `secrets.env.<NAME>` sets `from_env` to specify the source environment variable on the host.

When `jailoc up` runs:

1. Docker Compose reads the source environment variable from the host process and writes its value into an in-memory secret file mounted at `/run/secrets/<NAME>` inside the container.
2. `jailoc` generates a manifest file at `/etc/jailoc/secret-env` inside the container. This file contains `NAME NAME` pairs mapping each secret name to its container environment variable name. The manifest contains variable names only, never secret values.
3. Container startup begins with `entrypoint.sh` executing as root before privileges are dropped.
4. `entrypoint.sh` reads `/etc/jailoc/secret-env`, reads the value from `/run/secrets/<NAME>`, and populates an environment variable array.
5. `entrypoint.sh` passes the environment variable array to `setpriv` when executing the agent process:

    ```bash
    exec setpriv --reuid=1000 --regid=1000 --init-groups --inh-caps=-all --no-new-privs \
        -- env HOME=/home/agent "${secret_envs[@]}" "$@"
    ```

6. The agent process, running as UID 1000, inherits the secret as an environment variable named `<NAME>`.

Every environment secret is unconditionally exported as a container environment variable.

### 2. File Secrets (`secrets.file.<NAME>`)

File secrets mount a host file into the container filesystem. A secret configured under `secrets.file.<NAME>` sets `from_file` to specify the file path on the host.

When `jailoc up` runs:

1. Docker Compose creates a read-only bind mount from the host path to `/run/secrets/<NAME>` inside the container.
2. The mounted file retains the host file's permissions and mode bits.
3. The secret is accessible exclusively as a file at `/run/secrets/<NAME>`. File secrets are never exported as environment variables.

---

## Permissions and Host Inspection

Because the agent process runs as unprivileged user UID 1000, secret files mounted into the container must be accessible to that user.

### File Permissions

File secrets preserve the mode bits of the host file. Host-to-container UID mapping depends on the operating system and container runtime, such as virtiofs on macOS or user namespaces on Linux. Because `jailoc` cannot predict runtime UID mapping during configuration validation, it enforces a conservative permission check:

- The host file specified by `from_file` must be a regular file and world-readable (`others` read bit `o+r`).
- If the file lacks world-readable permissions, `jailoc up` rejects the configuration.

### Host Environment Variable Inspection

Environment secrets depend on host environment variables. When a host variable is empty or unset, Docker Compose skips creating the `/run/secrets/<NAME>` file inside the container without raising an error.

To prevent silent missing file errors inside the container, `jailoc` validates host environment variables before starting containers:

- If the host variable named in `from_env` is unset, validation fails.
- If the host variable is set to an empty string, validation fails.

---

## Secret Validation Summary

Validation occurs in two phases. Structural rules are checked when configuration is loaded. Source inspection rules are checked when starting a workspace container (`jailoc up` or `jailoc add`).

### Structural Rules

1. **Category Separation**: A secret must be declared under either `secrets.env` or `secrets.file`.
2. **Secret Name Grammar**: The section name `<NAME>` must match `^[a-zA-Z0-9_-]+$`.
3. **Environment Identifier Grammar**: For environment secrets, the section name `<NAME>` must match `^[A-Za-z_][A-Za-z0-9_]*$` and must not conflict with reserved environment variables (`HOME`, `DOCKER_HOST`, `OPENCODE_SERVER_PASSWORD`), workspace `env` entries, or other environment secret names.

### Source Inspection Rules

1. **Environment Sources**: The host environment variable specified by `from_env` must exist and contain a non-empty string.
2. **File Sources**: The file path specified by `from_file` must exist, be a regular file, and be world-readable.
