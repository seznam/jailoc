# Troubleshooting

Find the symptom, then apply the fix. Start with [Find the logs](#find-the-logs) when it isn't listed.

| Symptom | Section |
|---------|---------|
| Command fails on a v1 setting | [Config rejects a jailoc v1 setting](#config-rejects-a-jailoc-v1-setting) |
| No VM runtime for this build | [VM runtime is missing](#vm-runtime-is-missing) |
| `/dev/kvm` unavailable | [VM can't open /dev/kvm](#vm-cant-open-devkvm) |
| Landlock confinement error | [VM process can't be confined](#vm-process-cant-be-confined) |
| `jailoc up` times out or the VM supervisor exits | [VM doesn't become ready](#vm-doesnt-become-ready) |
| Builds or language servers die, VM stops under load | [Processes die or the VM stops when memory runs out](#processes-die-or-the-vm-stops-when-memory-runs-out) |
| Mount error at start | [A mount fails at start](#a-mount-fails-at-start) |
| Agent can't reach an internal service | [Network rules block a required host](#network-rules-block-a-required-host) |
| `403` on an allowed host | [Request denied with `http-no-match`](#request-denied-with-http-no-match) |
| TLS error on a host with an `http` rule | [Certificate error on an inspected host](#certificate-error-on-an-inspected-host) |
| Image pull or build errors | [Image build fails](#image-build-fails) |
| `docker` missing or failing in the guest | [Docker inside the VM fails](#docker-inside-the-vm-fails) |
| OpenCode exits at startup | [OpenCode fails to start with a server or database error](#opencode-fails-to-start-with-a-server-or-database-error) |

## Find the logs

### Read the jailoc log

jailoc writes its own structured log to `~/.cache/jailoc/jailoc.log`. It rotates at startup above 5 MB to `jailoc.log.1`; if the rename fails, the file is truncated instead. Without a home directory, the log goes to `$TMPDIR/jailoc/jailoc.log`. Look for `level=ERROR` lines.

### Find where a slow start spends its time

The log lists the duration of each step: image builds, the import into the VM image cache, VM start and session start. VM boot steps also carry their details, such as `microvm.image.from_cache`. A step that failed is logged as `vm step failed`. An advisory problem that did not stop the boot, such as a failed non-required preflight check, is a `level=WARN` `vm step finished` line with an `error`.

```bash
grep -E 'step (finished|failed)' ~/.cache/jailoc/jailoc.log | tail -n 30
```

### Read the workspace logs

```bash
jailoc logs <workspace>
jailoc logs -f <workspace>   # follow
```

The output starts with the records of the workspace's last `jailoc up` (`up.log`), followed by the supervisor and guest console logs with the boot timings.

### Find the VM state files

Each workspace VM keeps its state in `~/.cache/jailoc-vm/workspaces/<workspace>/`:

| File | Content |
|------|---------|
| `up.log` | The log records of the last `jailoc up`: each step with its duration, Docker pulls and builds with every build step (`build step finished`, with `cached` and its duration), the image's layer count and size, and the export from Docker and layer conversion |
| `supervisor.log` | VM supervisor output: broker and VM start, readiness with the time each took, shutdown reason |
| `microvm/console.log` | Guest console: boot, SSH connections and processes the kernel killed when the VM ran out of memory |
| `broker.jsonl`, `broker.jsonl.1` | Network broker decisions, one JSON object per line, rotated at 16 MiB; read them with `jailoc traffic` |
| `guest/control/exec.jsonl` | Processes started in the guest and their exits; read it with `jailoc strace` |
| `guest/` | The internal share the guest can write: control files and persistent home directories (`guest/home`) |
| `guest-ro/` | The read-only internal share: configuration copies and staged snapshots |
| `state.json` | Supervisor PID and readiness |
| `inspect-ca.pem`, `inspect-ca.key` | The workspace's TLS inspection CA, created when a network rule has `http`; the key stays on the host and is deleted by `jailoc down --purge` |

### Inspect what runs inside the VM

```bash
jailoc traffic -f            # connections and DNS lookups, live
jailoc strace -n 50          # the last 50 processes started or failed
jailoc shell                 # a shell as the agent user
jailoc exec -- ps aux        # one command
```

## Config rejects a jailoc v1 setting

**Symptom:** any command fails with:

```text
mode is a jailoc v1 setting; run `jailoc migrate` to update the config
```

The same error appears for `backend = "docker"`, `password_mode` and `expose_port`.

**Fix:** run the migration, which rewrites these settings and carries over v1 Docker workspaces:

```bash
jailoc migrate --dry-run   # preview
jailoc migrate
```

See [Migrate from jailoc v1](migrate-from-v1.md).

## VM runtime is missing

### No runtime in this build

**Symptom:** `jailoc up` fails with:

```text
this jailoc build does not include the VM runtime for <os>/<arch>; use a release build of jailoc or set runner and lib_dir in the [vm] section of the config
```

**Fix:** release builds embed the runtime for macOS (Apple Silicon) and Linux (amd64, arm64), and the Nix package on Linux provides one. On another platform, or with a binary built from source, set `[vm] runner` and `[vm] lib_dir` to a runtime you built yourself. See [How to install jailoc](installation.md#build-from-source).

### Package runtime path is gone

**Symptom:** `jailoc up` fails with:

```text
VM runner <path> provided by this jailoc package is missing: <error>; reinstall jailoc or set runner and lib_dir in the [vm] section of the config
```

**Cause:** the `jailoc` binary was copied out of its Nix store path, or that store path was garbage-collected.

**Fix:** reinstall or rebuild the Nix package, or set `[vm] runner` and `[vm] lib_dir` explicitly.

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

**Symptom:** on Linux, the VM fails to start with `confining the VM process needs Landlock`. jailoc runs the VM process under Landlock and does not start a VM without it.

**Fix:** use Linux 5.19 or later and check that `landlock` appears in `/sys/kernel/security/lsm`; if it doesn't, add it to the `lsm=` kernel parameter and reboot. See [How to install jailoc](installation.md#check-your-platform).

## VM doesn't become ready

**Symptom:** `jailoc up` fails with `vm not ready after ...`, `vm supervisor exited ...`, `VM NIC did not connect within ...` or `broker not ready after ...`.

**Diagnose:**

```bash
jailoc logs <workspace>
tail -n 50 ~/.cache/jailoc-vm/workspaces/<workspace>/supervisor.log
```

**Cause:** one of:

- the hypervisor isn't available (see [/dev/kvm](#vm-cant-open-devkvm) on Linux);
- too little memory for the requested `memory`;
- a custom image without the UID 1000 `agent` user or the agent binary (see [How to use a custom image](custom-images.md#know-what-the-image-must-provide));
- a stale VM left over from a crash.

**Fix:** remove the cause. Clear a stale VM with `jailoc down <workspace>` and start again.

## Processes die or the VM stops when memory runs out

**Symptom:** builds, tests or a language server die without an error of their own, or `jailoc logs` reports `vm stopped: workspace vm stopped: nic-disconnected` while the VM was busy.

**Diagnose:** the guest copies each kernel OOM kill to the console log:

```bash
jailoc logs <workspace> | grep 'out of memory'
jailoc strace -n 200 | grep SIGKILL   # processes that ended with SIGKILL
```

A line such as `msg="out of memory: kernel killed a process" pid=271 process=gopls anon_rss_mib=1536 oom_score_adj=0` names the victim and its memory use. See [Memory pressure](../explanation/architecture.md#memory-pressure) for the order in which the kernel picks victims.

**Fix:** give the workspace more memory with `memory` in its config (see [Configuration reference](../reference/configuration.md)), or run fewer memory-hungry jobs at once, for example `go test -p 1` or a lower `GOMAXPROCS`.

## A mount fails at start

**Symptom:** `jailoc up` fails with `mount "...": backend "vm" shares directories only; use snapshots for a read-only copy of a file`.

**Fix:** an entry in `mounts` is a file, and mounts are directory shares. Move file entries from `mounts` to `snapshots`. See [Snapshot files into the workspace](workspace-configuration.md#snapshot-files-into-the-workspace).

## Network rules block a required host

**Symptom:** the agent can't reach an internal service; connections are refused or time out.

**Cause:** private and reserved addresses, IPv4 and IPv6 (including ULA `fc00::/7`), are blocked by default. `jailoc traffic` shows every denied connection with its reason. On macOS, local-network hosts also need the Local Network permission (see [Allow local-network hosts on macOS](network-access.md#allow-local-network-hosts-on-macos)).

**Fix:** add the host or range, then reload:

```toml
[workspaces.myproject]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.10.5.0/24"]
```

```bash
jailoc reload myproject
```

For the rule form (`[[workspaces.<name>.network.rules]]`), see [Reach internal hosts by name](network-access.md#reach-internal-hosts-by-name) and [Allow a private IP or CIDR range](network-access.md#allow-a-private-ip-or-cidr-range). More recipes: [How to allow specific hosts or networks](network-access.md).

## Request denied with `http-no-match`

**Symptom:** a client gets `403 Forbidden` with `blocked by network policy (reason=http-no-match)` for a host that is allowed.

**Cause:** a rule with an `http` table matches the host. Once one does, only `http` allow rules decide its requests; a plain `allow` rule for the same host and port doesn't grant them, and the request's method or path matches none of them. Find the request with `jailoc traffic` (method, host, path, status, reason).

**Fix:** add the method or path to the `http` rule, or add `http.methods = ["*"]` or `http.paths = ["/**"]` to a more permissive rule for that host. Apply with `jailoc reload`. See [Restrict HTTP methods and paths](network-access.md#restrict-http-methods-and-paths).

Where an `http` rule with `paths` applies to the method, a path with `//`, `\`, `%5c`, `.` or `..` segments, or an encoded slash that forms a `..` or empty piece, is denied as `bad-path` instead. Without `paths` the path is not checked.

## Certificate error on an inspected host

**Symptom:** a client fails with a certificate or TLS handshake error for a host covered by a rule with an `http` table, and `jailoc traffic` shows an `inspect-error` line whose hint says the client doesn't trust the workspace inspection CA.

**Cause:** the broker decrypts these hosts with a certificate signed by the workspace's inspection CA. Clients that pin certificates, use their own CA store (certifi, a Java keystore, Firefox NSS) or authenticate with a client certificate (mTLS) don't accept it.

**Fix:** point the client at `/etc/ssl/certs/ca-certificates.crt`, or add the CA from there to its store. If that isn't possible, remove the `http` table from every rule matching the host; the connection is then passed through undecrypted. The CA is created again after `jailoc down --purge`, and clients that trusted the old one need the new certificate.

### `502` with a certificate hint

This is the other direction: the broker rejected the upstream certificate because it trusts only the host system roots and the `ca_bundle` path.

**Fix:** add the issuing CA to the host's trust store or set `ca_bundle` to a bundle that contains it.

## Image build fails

**Symptom:** `jailoc up` reports errors pulling or building the image.

The first matching image source wins, and a failure there is fatal. Common reasons:

- a registry that isn't reachable or needs authentication;
- a URL Dockerfile returning an HTTP error;
- a local Dockerfile path that doesn't exist;
- `[vm] builder = "docker"` without a usable host Docker daemon;
- the shared builder VM pulling from a private registry it can't reach (use `isolated_build = true` with the host in `allowed_hosts`).

**Diagnose:**

```bash
grep -Ei "image|pull|build|dockerfile" ~/.cache/jailoc/jailoc.log
```

See the [Image resolution reference](../reference/image-resolution.md).

## Docker inside the VM fails

**Symptom:** `docker` is not found in the guest, `jailoc up` fails with `enable_docker is on, but the workspace image has no dockerd`, or `docker` commands fail in the guest.

**Fix:** Docker is off by default; set `enable_docker = true` and run `jailoc restart`. An image used directly (`image`, or `defaults.image` without a workspace `dockerfile`) must contain Docker; install it there or set `enable_docker = false`. Images jailoc builds get Docker when `enable_docker` is on. Docker is also off when `protect_git = true`. See [Use Docker inside the VM](workspace-configuration.md#use-docker-inside-the-vm).

## OpenCode fails to start with a server or database error

**Symptom:** `jailoc` exits before the TUI appears with `Unexpected server error. Check server logs for details.` or `opencode exited with status 1`.

OpenCode batches writes to `~/.local/share/opencode/log/opencode.log`, so an immediate exit at startup may not flush the failure to the log file.

**Diagnose:** run OpenCode with `--print-logs` inside the workspace VM to print unbuffered logs and the underlying error to standard error:

```bash
jailoc -w <workspace> exec -- opencode --print-logs
jailoc -w <workspace> exec -- opencode debug config --print-logs
```

**Fix:** if the output reports a SQLite schema error (such as `no such column` after downgrading `agents.<name>.version` or sharing `~/.local/share/opencode` across incompatible OpenCode versions), upgrade the agent version to match the database schema, or stop any running sessions and move the database together with its WAL and SHM sidecars into a backup directory so OpenCode starts with an empty active database:

```bash
jailoc -w <workspace> exec -- sh -c 'bak=~/.local/share/opencode/db-backup-$(date +%Y%m%d-%H%M%S) && mkdir -p "$bak" && for f in opencode.db opencode.db-wal opencode.db-shm; do [ -e ~/.local/share/opencode/"$f" ] && mv ~/.local/share/opencode/"$f" "$bak"/; done'
```

## Clean up

Stop a workspace and delete its persistent home, Docker data and isolated builder:

```bash
jailoc down <workspace> --purge
```

`--purge` also deletes the workspace's TLS inspection CA key.

Remove the log file:

```bash
rm -f ~/.cache/jailoc/jailoc.log ~/.cache/jailoc/jailoc.log.1
```
