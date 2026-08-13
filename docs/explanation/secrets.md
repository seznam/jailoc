# Secrets Architecture

jailoc provides a secret management system for passing sensitive credentials—such as API keys, tokens, or certificate files—into workspace containers. It builds on Docker Compose's native secrets specification while extending it to handle environment variable injection and container file permissions safely.

For step-by-step setup instructions, see [How-to: Secrets](../how-to/secrets.md).

---

## The Two Secret Mechanisms

Docker Compose supports two underlying mechanisms for providing secrets to containers, depending on whether the secret source is an environment variable or a file on the host.

### 1. `environment`-sourced secrets

When a secret is configured with `env`, Docker Compose reads the specified environment variable from the host process when `jailoc up` runs. Compose packages the variable's string value into an in-memory tar archive and mounts it into the container at `/run/secrets/<name>`.

- **File ownership**: `root:root` inside the container
- **File permissions**: `0444` (world-readable)
- **Accessibility**: Because the permissions are `0444`, the secret file at `/run/secrets/<name>` is immediately readable by any container user, including the unprivileged agent process running as UID 1000.

### 2. `file`-sourced secrets

When a secret is configured with `file`, Docker Compose creates a read-only bind mount from the host file path to `/run/secrets/<name>` inside the container.

- **File ownership**: Preserves host UID/GID
- **File permissions**: Preserves host mode bits (e.g. `0600`, `0644`)
- **Accessibility**: Depends on host permissions and container runtime UID mapping.

---

## Permission Asymmetry and `expose_env`

The difference between `environment` and `file` secrets creates a critical permission asymmetry:

- `environment` secrets are always created with `0444` permissions, making them readable by UID 1000 without additional configuration.
- `file` secrets preserve host file permissions. A credential file on the host with restricted permissions—such as `0600` (`-rw-------`) owned by your host user—will retain `0600` inside the container.

Inside the container, the agent runs as unprivileged user UID 1000. If a host file with `0600` permissions is bind-mounted into the container, UID 1000 will receive a `Permission denied` error when trying to read `/run/secrets/<name>`.

### The `expose_env` escape hatch

To solve this permission restriction, jailoc supports the `expose_env` field. When `expose_env = "VAR_NAME"` is set on a secret:

1. Docker Compose mounts the secret at `/run/secrets/<name>` as usual.
2. jailoc writes a manifest file at `/etc/jailoc/secret-env` inside the container, listing secret names and target environment variable names (`<secret_name> <env_var_name>`).
3. Container startup begins. The container `entrypoint.sh` executes as **root** before dropping privileges.
4. `entrypoint.sh` reads `/etc/jailoc/secret-env`, reads each secret value from `/run/secrets/<secret_name>` (which root can read regardless of host file permissions), and appends the key-value pair to a Bash array.
5. `entrypoint.sh` passes the environment array directly to `setpriv` as part of the exec invocation:

    ```bash
    exec setpriv --reuid=1000 --regid=1000 --inh-caps=-all --no-new-privs \
        env "${secret_envs[@]}" opencode serve
    ```

6. The agent process (running as UID 1000) inherits the secret value in its environment variable (`VAR_NAME`), bypassing host file permission limitations. Nothing is exported into the root shell session.

---

## Up-Time Validation and Edge Cases

Rather than letting Docker Compose or container startup fail with cryptic errors, `jailoc up` and `jailoc add` validate all configured secret sources proactively on the host before any container is started.

### Rejection of empty host environment variables

When a secret references a host environment variable (`env = "VAR"`), Compose expects the variable to contain a non-empty string. If the host variable is set to an empty string (`""`), Docker Compose silently skips creating the `/run/secrets/<name>` file altogether without returning an error.

To prevent silent failures inside the container where `/run/secrets/<name>` unexpectedly does not exist, jailoc checks host environment variables during up-time validation:

- If the variable is unset, validation fails.
- If the variable is set but empty, validation fails as well: Compose omits empty secrets, so `/run/secrets/<name>` would not be created.

### Conservative world-readable check for file secrets

When a secret uses `file` without `expose_env`, the file must be readable by the container's unprivileged user (UID 1000).

However, host-to-container UID mapping varies depending on the operating system and container runtime (for example, virtiofs or grpcfuse on macOS vs user namespaces on Linux). Because jailoc cannot know at validation time how the container runtime will map host UIDs inside the container, it applies a **conservative check**:

- If `expose_env` is **not** set, jailoc verifies that the host file is world-readable (`others` read bit `o+r`).
- If the file is not world-readable, `jailoc up` fails and reports the offending permissions, explaining that `expose_env` can be set instead to make the secret readable by the container user.

Setting `expose_env` acts as the explicit signal that the secret will be read by root during entrypoint execution and re-exported into an environment variable, bypassing the world-readable requirement.

---

## Secret Validation Summary

Validation happens in two tiers. Structural rules (exactly one of `env` or `file`, the secret-name format, and the `expose_env` variable-name and reserved-name rules) are checked whenever configuration is loaded. Source rules that inspect the host (environment-variable presence and the secret file's existence, type, and permissions) are checked when a container starts (`jailoc up`, and `jailoc add` for an already-running workspace).

1. **XOR Rule**: Exactly one of `env` or `file` must be specified for each secret.
2. **Secret Name**: Must match `^[a-zA-Z0-9_-]+$`.
3. **Target Env Name**: If `expose_env` is set, the variable name must match `^[A-Za-z_][A-Za-z0-9_]*$` and must not conflict with reserved environment variables (`HOME`, `DOCKER_HOST`, `OPENCODE_SERVER_PASSWORD`, etc.), workspace `env` entries, or another secret's `expose_env`.
4. **Source Existence & Permissions**:
    - For `env` sources: host variable must be set and non-empty.
    - For `file` sources: path must exist, be a regular file, and be world-readable unless `expose_env` is set.
