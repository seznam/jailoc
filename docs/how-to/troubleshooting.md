# Troubleshooting

Common issues and debugging steps for jailoc workspaces.

---

## Find log files

jailoc writes structured logs to:

```
~/.cache/jailoc/jailoc.log
```

The log file rotates at startup when it exceeds 5 MB — the previous file is renamed to `jailoc.log.1`. If the rename fails, the log file is truncated instead (so `.1` may not exist on every system). If your home directory cannot be determined, logs fall back to `$TMPDIR/jailoc/jailoc.log`. If the log file still cannot be opened, logging is silently disabled.

All log entries use `slog` text format with timestamps, levels, and key-value pairs. Look for `level=ERROR` lines to find failures.

---

## Docker daemon not running

**Symptom:** `jailoc up` fails immediately with a Docker connection error.

**Fix:** Start the Docker daemon:

```bash
# Linux (systemd)
sudo systemctl start docker

# macOS (Docker Desktop)
open -a Docker
```

Verify with `docker info` before retrying.

---

## Port conflicts

**Symptom:** `jailoc up` fails with "address already in use" or the container starts but attach fails with connection refused.

Ports are assigned as `4096 + alphabetical index` among all configured workspaces. If another process occupies that port:

```bash
# Find what's using the port (e.g. 4096)
lsof -i :4096
```

**Fix:** Stop the conflicting process, or add/remove/rename workspaces to shift the port assignment. Run `jailoc status` to see the assigned port for each workspace.

---

## Permission denied on bind mounts

**Symptom:** Container starts but the agent reports permission errors reading or writing mounted paths.

The agent runs as UID 1000 inside the container. The mounted host directories must be readable (and writable, for workspace paths) by UID 1000.

**Fix:**

```bash
# Check ownership
ls -ln /path/to/mounted/dir

# Fix if needed
sudo chown -R 1000:1000 /path/to/mounted/dir
```

!!! note
    On macOS with Docker Desktop, file sharing handles UID mapping automatically. This issue primarily affects Linux hosts.

---

## Network restrictions blocking required hosts

**Symptom:** The agent cannot reach an internal service (registry, MCP server, API) — connections time out or get refused.

jailoc blocks all RFC 1918, link-local, and CGNAT addresses by default. If a required service lives on a private address, it must be explicitly allowed.

**Fix:** Add the host or network to your workspace config:

```toml
[workspaces.myproject]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.10.5.0/24"]
```

Then restart the workspace:

```bash
jailoc restart myproject
```

See [How to allow specific hosts or networks](network-access.md) for details.

---

## Image pull failures

**Symptom:** `jailoc up` reports errors pulling or building the container image.

Image resolution uses a priority cascade — the first matching configuration wins, and build/pull failures at that step are fatal. Common causes:

- Registry unreachable (network issues, auth required)
- URL Dockerfile returns HTTP errors
- Local Dockerfile path does not exist

**Debugging:**

```bash
# Check the log for image resolution errors
grep -Ei "image|pull|build|dockerfile" ~/.cache/jailoc/jailoc.log
```

See [Image resolution reference](../reference/image-resolution.md) for the full cascade order.

---

## Container does not start

**Symptom:** `jailoc up` completes but `jailoc status` shows the workspace is not running.

**Debugging:**

```bash
# Check container logs for startup errors
jailoc logs <workspace>
```

Common causes:

- Entrypoint script fails (iptables errors if running without required capabilities)
- Bind-mount source path does not exist on host
- Insufficient disk space for volumes

---

## Attach fails or connection refused

**Symptom:** `jailoc attach` exits immediately or reports connection refused.

**Debugging:**

1. Confirm the workspace is running:

    ```bash
    jailoc status
    ```

2. Check that the opencode process started inside the container:

    ```bash
    jailoc logs <workspace>
    ```

3. Verify the port is listening:

    ```bash
    lsof -i :<port>
    ```

If the container is running but opencode did not start, check the container logs for entrypoint errors (e.g. permission issues, missing config).

---

## DinD sidecar not healthy

**Symptom:** Docker commands inside the workspace fail — the agent cannot build images or run containers.

The DinD (Docker-in-Docker) sidecar runs a separate Docker daemon on TLS port 2376. If it is unhealthy:

```bash
# Check sidecar logs
jailoc logs <workspace>
```

Common causes:

- Host does not support privileged containers (some CI environments)
- TLS certificate volume not properly shared between containers
- Insufficient disk space for Docker data volume

---

## Cleanup stale resources

Over time, stopped workspaces may leave behind containers, volumes, and cached files.

### Remove cached compose files

```bash
rm -rf ~/.cache/jailoc/<workspace>/
```

### Remove jailoc Docker resources

```bash
# Stop each workspace first (repeat for every running workspace)
jailoc down <workspace>

# Remove exited jailoc containers
docker ps -a --filter "label=com.docker.compose.project" --filter "status=exited" \
  --format '{{.Names}}' | grep '^jailoc-' | xargs docker rm

# Remove jailoc volumes (all workspaces must be stopped)
docker volume ls --format '{{.Name}}' \
  | grep '^jailoc-' | xargs docker volume rm
```

### Remove the log file

```bash
rm -f ~/.cache/jailoc/jailoc.log ~/.cache/jailoc/jailoc.log.1
```
