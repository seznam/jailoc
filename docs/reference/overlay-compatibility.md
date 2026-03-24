# Overlay compatibility

This page describes which Dockerfile instructions are inherited from a parent image, which jailoc overrides at runtime, and which overlay changes break container functionality.

## Docker instruction inheritance

When an overlay Dockerfile uses `FROM <parent>`, the following instructions are either carried forward from the parent or replaced by the overlay layer.

| Instruction | Inherited | Notes |
|-------------|-----------|-------|
| `ENV` | Yes | Overlay can extend or override individual variables |
| `EXPOSE` | Yes | All exposed ports from the parent remain declared |
| `VOLUME` | Yes | Parent-declared volumes persist; see incompatible changes below |
| `LABEL` | Yes | Parent labels are merged; overlay labels take precedence on collision |
| `ENTRYPOINT` | Overridden | Compose template forces `/usr/local/bin/entrypoint.sh` at runtime |
| `CMD` | Overridden | Compose template forces `opencode serve ...` at runtime |
| `USER` | Overridden | Entrypoint drops to UID 1000 via `setpriv`; `USER` instruction has no effect |
| `WORKDIR` | Overridden | Compose template sets `working_dir` to the first workspace path at runtime |
| `SHELL` | Overridden | Entrypoint runs under `/bin/sh` regardless of `SHELL` declaration |
| `HEALTHCHECK` | Overridden | No healthcheck is applied; the Compose template does not preserve it |
| `STOPSIGNAL` | Overridden | Default `SIGTERM` applies; overlay `STOPSIGNAL` declarations are ignored |

## jailoc runtime overrides

The generated `docker-compose.yml` unconditionally sets the following fields for the `opencode` service, regardless of what the Dockerfile declares.

| Compose field | Value | Source |
|---------------|-------|--------|
| `entrypoint` | `["/usr/local/bin/entrypoint.sh"]` | Compose template |
| `command` | `["opencode", "serve", ...]` | Compose template |
| `working_dir` | First workspace path from config | Resolved workspace |

These values cannot be changed by modifying the overlay Dockerfile. They are rendered from the embedded compose template at `jailoc up` time.

## Incompatible changes

The table below lists overlay actions that affect runtime behaviour. Severity levels apply regardless of whether the action is intentional.

| Action | Severity | Effect |
|--------|----------|--------|
| Delete `/usr/local/bin/entrypoint.sh` | Fatal | Container fails to start; entrypoint binary is missing |
| Delete `/home/agent` or UID 1000 user | Fatal | Entrypoint `chown` and `setpriv` calls fail; container exits before agent starts |
| Remove `iptables` package | Fatal | Network isolation setup fails; entrypoint exits with error |
| Remove `setpriv` package | Fatal | Privilege drop from root to UID 1000 fails; entrypoint cannot continue |
| Override `ENV PATH` without `/home/agent/.local/bin` | Breaking | OpenCode and installed tools are not found on `PATH` |
| Add `VOLUME` on workspace mount paths | Breaking | Docker named volumes shadow the bind-mount; workspace writes do not reach the host |
| Remove `sudo` package | Degraded | Agent cannot install packages at runtime; existing functionality unaffected |
| Override `ENTRYPOINT` | Safe | Compose template overrides at runtime; Dockerfile declaration is ignored |
| Override `CMD` | Safe | Compose template overrides at runtime; Dockerfile declaration is ignored |
| Override `WORKDIR` | Safe | Compose template sets `working_dir` at runtime; Dockerfile declaration is ignored |

**Severity definitions:**

| Severity | Meaning |
|----------|---------|
| Fatal | Container fails to start or exits immediately |
| Breaking | Container starts but core functionality is unavailable |
| Degraded | Container starts; partial functionality is lost |
| Safe | No runtime effect; jailoc overrides the declaration |

## Related pages

- [Custom images](../how-to/custom-images.md) — how to build and configure overlay Dockerfiles
- [Container architecture](../explanation/container-architecture.md) — entrypoint phases, volume layout, and the DinD sidecar
- [Image resolution](image-resolution.md) — the two-tier model that resolves base and overlay images
