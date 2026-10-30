# Troubleshooting

Common failures of jailoc workspaces and how to fix them.

## Find the logs

jailoc writes its own structured log to `~/.cache/jailoc/jailoc.log`. It rotates at startup above 5 MB to `jailoc.log.1`; if the rename fails, the file is truncated instead. Without a home directory, the log goes to `$TMPDIR/jailoc/jailoc.log`. Look for `level=ERROR` lines.

To find out where a slow start spends its time, list the duration of each step: image builds, the import into the VM image cache, the per-boot rootfs clone, VM start and session start.

```bash
grep -E 'step finished' ~/.cache/jailoc/jailoc.log | tail -n 30
```

Each workspace VM keeps its state in `~/.cache/jailoc-vm/workspaces/<workspace>/`:

| File | Content |
|------|---------|
| `supervisor.log` | VM supervisor output: broker and VM start, readiness with the time each took, shutdown reason |
| `microvm/console.log` | Guest console: boot and SSH connections |
| `broker.jsonl` | Network broker decisions, one JSON object per line; read it with `jailoc traffic` |
| `guest/control/exec.jsonl` | Processes started in the guest; read it with `jailoc strace` |
| `guest/` | The internal share the guest can write: control files, configuration copies, snapshots, persistent home directories (`guest/home`) |
| `state.json` | Supervisor PID and readiness |

Print the two logs with:

```bash
jailoc logs <workspace>
jailoc logs -f <workspace>   # follow
```

Inspect what runs inside the VM:

```bash
jailoc traffic -f            # connections and DNS lookups, live
jailoc strace -n 50          # the last 50 processes started
jailoc shell                 # a shell as the agent user
jailoc exec -- ps aux        # one command
```

## Config rejects a jailoc v1 setting

**Symptom:** any command fails with:

```
mode is a jailoc v1 setting; run `jailoc migrate` to update the config
```

The same error appears for `backend = "docker"`, `password_mode` and `expose_port`.

**Fix:** run the migration, which rewrites these settings and carries over v1 Docker workspaces:

```bash
jailoc migrate --dry-run   # preview
jailoc migrate
```

See [Migrate from jailoc v1](migrate-from-v1.md).

## Platform isn't supported

**Symptom:** `jailoc up` fails with:

```
VM runtime is not supported on <os>/<arch> (supported: darwin/arm64, linux/amd64); set runner and lib_dir in the [vm] section of the config to use a self-built runtime
```

**Fix:** run jailoc on macOS (Apple Silicon) or Linux (amd64), or set `[vm] runner` and `[vm] lib_dir` to a runtime you built yourself. See [How to install jailoc](installation.md#check-your-platform).

## VM can't open /dev/kvm

**Symptom:** on Linux, the VM fails to start and `supervisor.log` reports that `/dev/kvm` is missing or permission is denied.

**Fix:**

- If `/dev/kvm` doesn't exist, enable virtualization (VT-x/AMD-V) in the firmware and load the `kvm_intel` or `kvm_amd` module. Inside a cloud VM, nested virtualization must be enabled.
- If it exists but isn't accessible, add your user to its group and log in again:

```bash
ls -l /dev/kvm
sudo usermod -aG kvm "$USER"
```

## VM process can't be confined

**Symptom:** on Linux, the VM fails to start with `confining the VM process needs Landlock`.

**Fix:** jailoc runs the VM process under Landlock and does not start a VM without it. Use Linux 5.19 or later and check that `landlock` appears in `/sys/kernel/security/lsm`; if it doesn't, add it to the `lsm=` kernel parameter and reboot. See [How to install jailoc](installation.md#check-your-platform).

## VM runtime download fails

**Symptom:** the first `jailoc up` fails while `Downloading VM runtime ...` with `download ...: HTTP <code>` or `checksum mismatch for ...`.

jailoc downloads a pinned runtime and checks its SHA-256 before unpacking it into `~/.cache/jailoc-vm/runtime/`. A failed or mismatching download is discarded and nothing is installed.

**Fix:** check that the host reaches GitHub (and your proxy settings), then run `jailoc up` again. A persistent checksum mismatch means the download is altered on the way, for example by a proxy that rewrites responses. Don't work around it; fix the network path.

## VM doesn't become ready

**Symptom:** `jailoc up` fails with `vm not ready after ...`, `vm supervisor exited ...`, `VM NIC did not connect within ...` or `broker not ready after ...`.

**Debugging:**

```bash
jailoc logs <workspace>
tail -n 50 ~/.cache/jailoc-vm/workspaces/<workspace>/supervisor.log
```

Common causes:

- the hypervisor isn't available (see [/dev/kvm](#vm-cant-open-devkvm) on Linux);
- too little memory for the requested `memory`;
- a custom image without the UID 1000 `agent` user or the agent binary (see [How to use a custom image](custom-images.md#know-what-the-image-must-provide));
- a stale VM left over from a crash.

Clear a stale VM with `jailoc down <workspace>` and start again.

## A mount fails at start

**Symptom:** `jailoc up` fails with `mount "...": backend "vm" shares directories only; use snapshots for a read-only copy of a file`.

**Fix:** move file entries from `mounts` to `snapshots`. See [Snapshot files into the workspace](workspace-configuration.md#snapshot-files-into-the-workspace).

## Network rules block a required host

**Symptom:** the agent can't reach an internal service; connections are refused or time out.

Private and reserved addresses, IPv4 and IPv6 (including ULA `fc00::/7`), are blocked by default.

**Fix:** add the host or range, then reload:

```toml
[workspaces.myproject]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.10.5.0/24"]
```

```bash
jailoc reload myproject
```

`jailoc traffic` shows every denied connection with its reason. On macOS, local-network hosts also need the Local Network permission. See [How to allow specific hosts or networks](network-access.md).

## Image build fails

**Symptom:** `jailoc up` reports errors pulling or building the image.

The first matching image source wins, and a failure there is fatal. Common causes:

- a registry that isn't reachable or needs authentication;
- a URL Dockerfile returning an HTTP error;
- a local Dockerfile path that doesn't exist;
- `[vm] builder = "docker"` without a usable host Docker daemon;
- the shared builder VM pulling from a private registry it can't reach (use `isolated_build = true` with the host in `allowed_hosts`).

```bash
grep -Ei "image|pull|build|dockerfile" ~/.cache/jailoc/jailoc.log
```

See the [Image resolution reference](../reference/image-resolution.md).

## Docker inside the VM fails

**Symptom:** `docker` is not found in the guest, `jailoc up` fails with `enable_docker is on, but the workspace image has no dockerd`, or `docker` commands fail in the guest.

**Fix:** Docker is off by default; set `enable_docker = true` and run `jailoc restart`. An image used directly (`image`, or `defaults.image` without a workspace `dockerfile`) must contain Docker; install it there or set `enable_docker = false`. Images jailoc builds get Docker when `enable_docker` is on. Docker is also off when `protect_git = true`. See [Use Docker inside the VM](workspace-configuration.md#use-docker-inside-the-vm).

## Clean up

Stop a workspace and delete its persistent home, Docker data and isolated builder:

```bash
jailoc down <workspace> --purge
```

Remove the log file:

```bash
rm -f ~/.cache/jailoc/jailoc.log ~/.cache/jailoc/jailoc.log.1
```
