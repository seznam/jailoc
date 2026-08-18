# Secrets Architecture

jailoc provides a secret management system for passing sensitive credentials, such as API keys, tokens, or certificate files, into workspace containers. It builds on Docker Compose's native secrets specification while managing environment variable injection and container file permissions.

For step-by-step setup instructions, see [How-to: Secrets](../how-to/secrets.md).

---

## The Destination × Source Model

Secrets in jailoc are defined along two independent axes: the destination axis and the source axis.

### Destination Axis

The destination axis determines how the secret is exposed inside the container. It is specified by selecting one of two workspace configuration sub-tables:

* **Environment Destination (`secrets.env.<NAME>`)**: Exposes the secret value as an environment variable named `<NAME>` inside the container.
* **File Destination (`secrets.file.<NAME>`)**: Exposes the secret value as a file at `/run/secrets/<NAME>` inside the container filesystem.

The section name `<NAME>` defines the output identity of the secret inside the container.

### Source Axis

The source axis determines where the secret value originates on the host. Each secret configuration sets exactly one source field:

* **Environment Source (`from_env`)**: Reads the secret value from a host environment variable.
* **File Source (`from_file`)**: Reads the secret value from a host file path.

---

## The Four Secret Configurations

Combining the destination axis and source axis yields four valid secret configurations.

### 1. Environment Destination + Environment Source (`secrets.env.<NAME>` with `from_env`)

The secret value is read from a host environment variable and exported as a container environment variable named `<NAME>`.

When starting a workspace container:

1. Docker Compose reads the source environment variable from the host process and writes its value into an in-memory secret file mounted at `/run/secrets/<NAME>` inside the container.
2. jailoc generates a manifest file at `/etc/jailoc/secret-env` inside the container. This manifest lists variable mappings without containing any secret values.
3. Container startup executes `entrypoint.sh` as root before privileges are dropped.
4. `entrypoint.sh` reads the value from `/run/secrets/<NAME>` and populates an environment variable array.
5. `entrypoint.sh` passes the environment variables to `setpriv` when starting the agent process:

    ```bash
    exec setpriv --reuid=1000 --regid=1000 --init-groups --inh-caps=-all --no-new-privs \
        -- env HOME=/home/agent "${secret_envs[@]}" "$@"
    ```

6. The agent process, running as UID 1000, inherits the secret as an environment variable named `<NAME>`.

### 2. Environment Destination + File Source (`secrets.env.<NAME>` with `from_file`)

The secret value is read from a host file and exported as a container environment variable named `<NAME>`.

When starting a workspace container:

1. Docker Compose creates a read-only bind mount from the host file path to `/run/secrets/<NAME>` inside the container.
2. `entrypoint.sh` executes as root during container startup, reads the secret value from `/run/secrets/<NAME>`, and adds it to the environment variable array.
3. `entrypoint.sh` passes the environment variable array to `setpriv` when executing the agent process.
4. The agent process (UID 1000) inherits the secret as an environment variable named `<NAME>`.

### 3. File Destination + Environment Source (`secrets.file.<NAME>` with `from_env`)

The secret value is read from a host environment variable and exposed as a file at `/run/secrets/<NAME>`.

When starting a workspace container:

1. Docker Compose materializes the host environment variable's value into a secret file mounted at `/run/secrets/<NAME>` inside the container.
2. Docker Compose sets the materialized file ownership to `root:root` with permissions `0444` (`-r--r--r--`).
3. The unprivileged agent process (UID 1000) reads the secret file directly at `/run/secrets/<NAME>`. The secret is not exported as an environment variable.

### 4. File Destination + File Source (`secrets.file.<NAME>` with `from_file`)

The host file is mounted directly into the container filesystem at `/run/secrets/<NAME>`.

When starting a workspace container:

1. Docker Compose creates a read-only bind mount from the host path to `/run/secrets/<NAME>` inside the container.
2. The mounted file retains the original permissions and mode bits from the host filesystem.
3. The secret is accessible exclusively as a file at `/run/secrets/<NAME>`. The secret is not exported as an environment variable.

---

## Permissions and Host Inspection

Because the agent process runs as unprivileged user UID 1000, secret files accessed inside the container must be readable by that user. However, host file permissions are only relevant when the unprivileged container process reads a bind-mounted host file directly.

### File Permissions

The four secret configurations handle file permissions differently based on how the secret reaches the container:

* **Combo 1 (Environment Destination + Environment Source)**: Involves no host file. The value is read from a host environment variable, so host file permissions do not apply.
* **Combo 2 (Environment Destination + File Source)**: Reads a host file, but container startup reads `/run/secrets/<NAME>` inside `entrypoint.sh` while running as root before dropping privileges through `setpriv`. Because root can read any host file mounted into the container, host file permissions do not restrict container startup.
* **Combo 3 (File Destination + Environment Source)**: Docker Compose materializes the host environment variable into a container file owned by `root:root` with mode `0444`. Because mode `0444` allows read access to all users, UID 1000 can read the file regardless of host settings.
* **Combo 4 (File Destination + File Source)**: **Requires the host file to be world-readable (`o+r`)**. Docker Compose bind-mounts the host file directly into the container, where the unprivileged agent process (UID 1000) reads it directly. Because the mounted file retains its host mode bits, the host file must have `others` read permission (`o+r`). If the host file lacks world-readable permissions, `jailoc up` rejects the configuration.

### Host Environment Variable Inspection

Configurations using an environment source (`from_env`) depend on host environment variables. When a host variable is empty or unset, Docker Compose skips creating the `/run/secrets/<NAME>` file inside the container without raising an error.

To prevent missing secret files inside the container, jailoc validates host environment variables before starting containers:

* If the host variable named in `from_env` is unset, validation fails.
* If the host variable is set to an empty string, validation fails.

---

## Secret Validation Summary

Validation occurs in two phases: structural rules checked when loading configuration, and source inspection rules checked when starting containers.

### Structural Rules

1. **Destination Sub-table Selection**: A secret must be declared under exactly one destination sub-table: `secrets.env` or `secrets.file`.
2. **Source Field Selection**: Each secret entry must specify exactly one source field: `from_env` or `from_file`.
3. **Secret Name Grammar**: The section name `<NAME>` must match `^[a-zA-Z0-9_-]+$`.
4. **Environment Identifier Grammar**: For secrets declared under `secrets.env`, the section name `<NAME>` must match `^[A-Za-z_][A-Za-z0-9_]*$` and must not conflict with reserved environment variables (`HOME`, `DOCKER_HOST`, `OPENCODE_SERVER_PASSWORD`), workspace `env` entries, or other environment secret names.

### Source Inspection Rules

1. **Environment Sources (`from_env`)**: The host environment variable specified by `from_env` must exist and contain a non-empty string.
2. **File Sources (`from_file`)**: The file path specified by `from_file` must exist and be a regular file. For file-destination secrets (`secrets.file.<NAME>` with `from_file`), the host file must also be world-readable (`o+r`).
