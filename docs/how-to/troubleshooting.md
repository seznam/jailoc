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

If the sidecar logs a `jailoc-dind: FATAL` line, the message identifies which startup check failed:

- **`could not install su-exec`**: The image lacks `su-exec` and could not install it from package repositories, often due to network isolation rules or missing connectivity. Install `su-exec` in a [custom image](custom-images.md) so startup does not require network access.
- **`unmarked substantive Docker data in ...; refusing to change storage backend`**: `dind-data` contains files or directories, but `/var/lib/jailoc/storage/storage-mode` is missing. Keep the data intact and [seed verified storage metadata](#seed-storage-metadata-for-existing-docker-data).
- **`native overlayfs was previously selected for this volume but is unavailable`**: The `native-snapshotter` or `legacy-overlay2` pin requires native overlayfs, but the current kernel cannot mount it as the rootless user. Restore a compatible kernel/runtime instead of switching to fuse over existing data.
- **`/home/rootless/.config/docker/daemon.json is not managed by jailoc or was modified`**: A daemon configuration file exists at that path but differs from the config jailoc manages, or was modified after creation. Inspect the file manually to reconcile differences.

The backend pin (`native-snapshotter`, `legacy-overlay2`, or `fuse-overlayfs`) lives on `dind-storage-meta` at `/var/lib/jailoc/storage/storage-mode`. Keep this volume paired with `dind-data` across recreations. Generated `/home/rootless/.config/docker/daemon.json` is container-local and is recreated from the pin when needed.

### Seed storage metadata for existing Docker data

When upgrading a workspace with populated `dind-data`, the metadata volume may be empty. Only seed it after establishing the **old daemon's effective backend**. Switching image stores hides existing images and containers; the current kernel probe and directory names cannot identify the old backend. Never delete `dind-data` to bypass this check.

1. While the old workspace daemon is still running, record its backend and inventory (the host's `docker info` is unrelated):

    ```bash
    WORKSPACE="<workspace>"
    PROJECT="jailoc-${WORKSPACE}"
    docker exec "${PROJECT}-opencode-1" docker info \
      --format 'Root={{.DockerRootDir}} Driver={{.Driver}} Status={{json .DriverStatus}}'
    docker exec "${PROJECT}-opencode-1" docker image ls --digests
    docker exec "${PROJECT}-opencode-1" docker ps -a
    ```

    Require `Root=/home/rootless/.local/share/docker`. Set `TARGET_BACKEND` to `native-snapshotter` **only** when `Driver=overlayfs` and Status reports `driver-type io.containerd.snapshotter.v1`; to `legacy-overlay2` when `Driver=overlay2` without that status; or to `fuse-overlayfs` when `Driver=fuse-overlayfs` without that status. Stop if the output is ambiguous. If the old daemon is unavailable, use trustworthy saved evidence or restore its previous runtime against a backup to collect `docker info`; do not seed from a guess.

2. Identify the old data volume from that workspace's DinD container, then stop the workspace and locate both named volumes. If the old container is gone, identify the data volume by its Compose labels instead of assuming a name. A first `jailoc up` with the new version creates `dind-storage-meta` and may report success even though DinD exits with the unmarked-data error; stop the workspace again before continuing.

    ```bash
    DATA_VOLUME=$(docker inspect "${PROJECT}-dind-1" \
      --format '{{range .Mounts}}{{if eq .Destination "/home/rootless/.local/share/docker"}}{{.Name}}{{end}}{{end}}')
    jailoc down "$WORKSPACE"
    jailoc up "$WORKSPACE"   # Creates metadata volume; DinD refuses unmarked data
    META_VOLUME=$(docker inspect "${PROJECT}-dind-1" \
      --format '{{range .Mounts}}{{if eq .Destination "/var/lib/jailoc/storage"}}{{.Name}}{{end}}{{end}}')
    jailoc down "$WORKSPACE"
    docker volume inspect "$DATA_VOLUME" "$META_VOLUME" \
      --format '{{.Name}} {{index .Labels "com.docker.compose.project"}} {{index .Labels "com.docker.compose.volume"}}'
    ```

    Confirm the two labels are respectively `${PROJECT} dind-data` and `${PROJECT} dind-storage-meta`. Abort if a name is empty, either label differs, or the volumes are not the intended pair. Back up both volumes while stopped; do not mount the data volume writable in a helper container. The following uses the locally cached DinD image and creates a backup directory under your home directory:

    ```bash
    BACKUP_DIR=$(mktemp -d "${HOME}/jailoc-${WORKSPACE}-dind-backup.XXXXXX")
    docker run --rm --pull=never --network none --user 0:0 \
      --mount "type=volume,src=${DATA_VOLUME},dst=/data,readonly" \
      --mount "type=bind,src=${BACKUP_DIR},dst=/backup" \
      --entrypoint tar docker:dind-rootless -czf /backup/dind-data.tar.gz -C /data .
    docker run --rm --pull=never --network none --user 0:0 \
      --mount "type=volume,src=${META_VOLUME},dst=/meta,readonly" \
      --mount "type=bind,src=${BACKUP_DIR},dst=/backup" \
      --entrypoint tar docker:dind-rootless -czf /backup/dind-meta.tar.gz -C /meta .
    ```

3. Seed the **empty** metadata volume with the verified value. Use a locally trusted `docker:dind-rootless` image. This helper has no network and mounts only the metadata volume; it rejects existing files, including hidden files and symlinks. Set `TARGET_BACKEND` to the value determined in step 1:

    ```bash
    TARGET_BACKEND="legacy-overlay2"  # Replace with verified mode
    docker run --rm --pull=never --network none --user 0:0 \
      --mount "type=volume,src=${META_VOLUME},dst=/meta" \
      --env "TARGET_BACKEND=$TARGET_BACKEND" --entrypoint sh docker:dind-rootless -c '
        set -eu
        case "$TARGET_BACKEND" in
          native-snapshotter|legacy-overlay2|fuse-overlayfs) ;;
          *) echo "Invalid storage backend" >&2; exit 1 ;;
        esac
        [ "$(stat -c %u /meta)" = 0 ] && [ -z "$(find /meta -mindepth 1 -print -quit)" ] || {
          echo "Metadata volume must be root-owned and empty" >&2; exit 1;
        }
        tmp=$(mktemp /meta/.storage-mode.XXXXXX)
        printf "%s\n" "$TARGET_BACKEND" > "$tmp"
        chmod 600 "$tmp"
        mv -n "$tmp" /meta/storage-mode
      '
    ```

4. Restart and confirm the selected backend and the recorded images and containers remain visible:

    ```bash
    jailoc up "$WORKSPACE"
    docker logs "${PROJECT}-dind-1"
    docker exec "${PROJECT}-opencode-1" docker info \
      --format 'Driver={{.Driver}} Status={{json .DriverStatus}}'
    docker exec "${PROJECT}-opencode-1" docker image ls --digests
    docker exec "${PROJECT}-opencode-1" docker ps -a
    ```

    Keep `dind-data` and `dind-storage-meta` together on future recreations. A pinned `legacy-overlay2` or `native-snapshotter` backend requires a kernel that supports rootless overlayfs.

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
