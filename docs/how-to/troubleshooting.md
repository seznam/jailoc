# Troubleshooting

Find the symptom, then apply the fix. Start with [Find the logs](#find-the-logs) when it isn't listed.

| Symptom | Section |
|---------|---------|
| Command fails on an unsupported setting | [Unsupported configuration](#unsupported-configuration) |
| No VM runtime for this build | [VM runtime is missing](#vm-runtime-is-missing) |
| `/dev/kvm` unavailable | [VM can't open /dev/kvm](#vm-cant-open-devkvm) |
| Landlock confinement error | [VM process can't be confined](#vm-process-cant-be-confined) |
| `jailoc up` times out or the VM supervisor exits | [VM doesn't become ready](#vm-doesnt-become-ready) |
| Builds or language servers die, VM stops under load | [Processes die or the VM stops when memory runs out](#processes-die-or-the-vm-stops-when-memory-runs-out) |
| Mount error at start | [A mount fails at start](#a-mount-fails-at-start) |
| Agent can't reach an internal service | [Network rules block a required host](#network-rules-block-a-required-host) |
| `403` on an allowed host | [Request denied with `http-no-match`](#request-denied-with-http-no-match) |
| TLS error on a host with an `http` rule | [Certificate error on an inspected host](#certificate-error-on-an-inspected-host) |
| `jailoc auth` finds no browser login link | [Browser login link is missing](#browser-login-link-is-missing) |
| Image pull or build errors | [Image build fails](#image-build-fails) |
| Build fails with `no space left on device`, or the builder VM is lost at startup | [Builder VM disk is full](#builder-vm-disk-is-full) |
| `jailoc up` fails with `no space left on device` on the state or cache disk | [State or cache disk is full](#state-or-cache-disk-is-full) |
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
| `microvm/console.log`, `.1`, `.2` | Guest console: boot, SSH connections and processes the kernel killed when the VM ran out of memory, rotated at 1 MiB (lines over 16 KiB are dropped and secrets are redacted); read them with `jailoc logs` |
| `broker.jsonl`, `broker.jsonl.1` | Network broker decisions, one JSON object per line, rotated at 16 MiB; read them with `jailoc traffic` |
| `guest/control/exec.jsonl` | Processes started in the guest and their exits; read it with `jailoc strace` |
| `guest/` | The internal share the guest can write: control files such as `guest/control/exec.jsonl` |
| `state.img` | The state disk (`state_disk`, 10 GiB by default): the agent's persistent home directories |
| `cache.img` | The cache disk (`cache_disk`, 20 GiB by default): `~/.cache` |
| `legacy-home/`, `legacy-home-<n>/` | The persistent home directories of a workspace that stored them as a directory, kept after the guest copied the agent state to `state.img`. The old `~/.cache` is not copied to `cache.img`. jailoc doesn't delete it |
| `guest-ro/` | The read-only internal share: configuration copies and staged snapshots |
| `state.json` | Supervisor PID and readiness |
| `oauth.sock` | The broker's private socket (mode `0600`, in the `0700` state directory) that `jailoc auth` reads pending browser login links from; the guest has no access to it |
| `inspect-ca.pem`, `inspect-ca.key` | The workspace's TLS inspection CA, created when a network rule has `http`; the key stays on the host and is deleted by `jailoc down --purge` |

### Inspect what runs inside the VM

```bash
jailoc traffic -f            # connections and DNS lookups, live
jailoc strace -n 50          # the last 50 processes started or failed
jailoc shell                 # a shell as the agent user
jailoc exec -- ps aux        # one command
```

<a id="config-rejects-a-jailoc-v1-setting"></a>

## Unsupported configuration

**Symptom:** any command fails with an error that names an unsupported setting.

**Fix:** see [Upgrade an existing installation](migrate-from-v1.md#recognize-a-v1-configuration).

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

**Symptom:** the VM fails to start because jailoc can't confine the VM process. jailoc runs the VM process under Landlock on Linux and under a Seatbelt profile on macOS, and does not start a VM without it.

- On Linux, the error is `confining the VM process needs Landlock`.
- On macOS, the error contains `apply Seatbelt profile:`.

**Fix:**

- On Linux, use Linux 5.19 or later and check that `landlock` appears in `/sys/kernel/security/lsm`; if it doesn't, add it to the `lsm=` kernel parameter and reboot. See [How to install jailoc](installation.md#check-your-platform).
- On macOS, read the exact error in `supervisor.log`. The profile is applied with `sandbox_init`, which Apple marks as deprecated but which remains available on current macOS; deprecation alone does not explain this error. Check the nested error for runner configuration or permission problems, and report unexplained failures in a [GitHub issue](https://github.com/seznam/jailoc/issues).

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

## VM doesn't start: `needs mkfs.ext4`

**Symptom:** `jailoc up` fails with `needs mkfs.ext4 (e2fsprogs) in the workspace image`, naming `state.img` or `cache.img`, or with an error naming `mkfs.ext4` for `/tmp`.

**Cause:** the guest formats the state and cache disks, and a disk-backed `/tmp`, with `mkfs.ext4` from the workspace image. An image without `e2fsprogs` has neither `mkfs.ext4` nor `resize2fs`.

**Fix:** add `e2fsprogs` to your base or overlay Dockerfile and run `jailoc up` again. See [what the image must provide](custom-images.md#know-what-the-image-must-provide).

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

## Browser login link is missing

**Symptom:** the host browser doesn't open when a tool in the workspace starts a browser login, the tool reports that opening the browser failed, or `jailoc auth` fails with `no pending sign-in`, `has no sign-in socket`, or `the sign-in for callback port N expired`.

| Message or cause | Fix |
|------------------|-----|
| Browser doesn't open, the opener in the guest exits non-zero | The host launch failed: `xdg-open` isn't in `PATH` on a Linux host, or `jailoc up` inherited no desktop session. Within 3 minutes run `jailoc auth <workspace> --open` from a terminal on the host desktop. A fake `DISPLAY` doesn't help. |
| Linux VM started from a headless SSH session | The workspace supervisor has no desktop environment. Run `jailoc restart <workspace>` from the desktop session, then start the login again. |
| Browser doesn't open, but `jailoc auth` finds the link | Several causes are possible, such as a workspace started before an upgrade or no usable opener on the host desktop. Run `jailoc restart <workspace>` for a workspace started before an upgrade, or run `jailoc auth <workspace> --open` from a terminal on the host desktop. |
| `has no sign-in socket` | The running broker doesn't provide `oauth.sock`. Run `jailoc restart <workspace>`, then start the login again. |
| `expired` | Start the login again in the workspace. If the browser doesn't open, run `jailoc auth` within 3 minutes. |
| `no pending sign-in` | The tool didn't call `xdg-open` or `$BROWSER` with an explicit `http` loopback `redirect_uri`, or no guest listener was active on that port, or a `ports` entry for the callback port is configured (it opens the browser but isn't listed). A tool with a device code or manual-code flow needs that flow. |
| `several sign-ins are pending` | Select one with `--port`. |

`jailoc auth` is only for retry and recovery. A normal login needs no command.

To see what the broker did, look for `OAUTH` lines:

```bash
jailoc traffic myproject --lines 200 | grep OAUTH
```

`open` means a callback listener was registered, `expired` means nobody called it within 3 minutes, and `served` means the callback was claimed, not necessarily that the token exchange succeeded. The lines show the host, port and path, never the query.

See [Complete browser login](workspace-configuration.md#complete-browser-login).

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

## Builder VM disk is full

**Symptom:** a build in a builder VM fails with `no space left on device`, or `jailoc up` fails while the builder VM starts, naming the builder's console log.

**Diagnose:** the builder VM keeps its own logs, separate from `jailoc logs`, which covers workspaces only:

| Builder | Directory |
|---------|-----------|
| Shared | `~/.cache/jailoc-vm/builder/` |
| `isolated_build` of a workspace | `~/.cache/jailoc-vm/workspaces/<workspace>/builder/` |

```bash
tail -n 100 ~/.cache/jailoc-vm/builder/microvm/console.log
tail -n 50 ~/.cache/jailoc-vm/builder/supervisor.log
cat ~/.cache/jailoc-vm/builder/state.json
```

For an isolated builder, use the second directory. `jailoc up` stops waiting as soon as the builder's VM runner, network broker or NIC connection is lost, and names the console log. When the state or the console contains `no space left on device`, the error also quotes the guest's last `df` lines and names the data disk. It calls the Docker data filesystem full only when the console shows it as of its latest lines: the failing path is below `/var/lib/docker`, or the latest `df` of `/var/lib/docker` has no free blocks or no free inodes (judged separately, so a healthy report of one doesn't clear the other, and a later healthy report clears an earlier failure). An error on exactly `/var/lib/docker` stays ambiguous, because that directory also exists on the guest root filesystem before the data disk is mounted. Otherwise the text can come from the guest root filesystem, which is backed by host storage, or from a full host disk, and the error says to check the host's free space and to raise the setting only if the data disk turns out to be full. A lost builder or a failed liveness check alone doesn't prove the disk is full; other causes end the same way.

The guest init writes `df -k` and `df -i` of the data disk to the console after mounting it, after growing it and when `dockerd` exits unexpectedly, together with the `dockerd` exit status. Tell the capacities apart:

- **Host disk:** `df -h ~/.cache/jailoc-vm` on the host. The builder's `docker.img` is a sparse file, so a full host disk fails writes on the host, and the error text can match. The guest's root filesystem is host-backed as well, so an out-of-space error on a path outside `/var/lib/docker` (for example `/usr/lib`) points here, not at the Docker disk.
- **Guest capacity:** the `df -k` lines on the console. A builder `docker.img` of 20 GiB (the default) holds the build cache and every built image; the guest can be full while the host has plenty of space.
- **Guest inodes:** the `df -i` lines. Many small files can use up the inodes before the blocks.

**Fix:** jailoc maintains a builder's cache before and after builds, but a daemon on a full disk can't run to prune. If the host disk is full, free host space; that alone fixes it, and growing the Docker disk would only let the sparse file take more of it. Grow the builder disk only when the guest `df` shows the data filesystem full, of blocks or of inodes:

```toml
[vm]
builder_disk = "40g"   # shared builder
```

For an isolated builder, raise the workspace's `docker_disk` instead.

```bash
jailoc up <workspace>
```

The disk and its file system grow and keep their content; they never shrink. Pick a size the host disk can hold, since the sparse file can grow up to it.

The guest init compares the data disk's file system capacity with its device before it installs the resize tool. A disk whose file system already fills it, including the intentional 1 MiB tail of a freshly formatted disk, needs neither the resize packages nor the network, so an existing cache that holds only `nftables` starts offline with the disk unchanged. Actual growth needs `resize2fs`, which comes from a complete persisted APK cache or from the package network. A missing tool or a failed resize is fatal. When growth is required and the builder is offline, give it access to the package network instead of resetting its data.

**Optional: start with an empty builder disk.** Only the builder's `docker.img` is moved aside here. The cache, built images and the Docker daemon's state in it are lost to the builder, and nothing is wiped for you. Keep the builder directory and its lock file, `builder.lock`, which sits next to the directory and not inside it: `~/.cache/jailoc-vm/builder.lock` for the shared builder, `~/.cache/jailoc-vm/workspaces/<workspace>/builder.lock` for an isolated one. Do not touch workspace homes.

jailoc stops a builder VM when its build ends, so a running builder supervisor means a `jailoc up` is building or a supervisor was left behind. Make sure no `jailoc up` is running, then check that no supervisor of that builder is alive. The command prints nothing when there is none:

```bash
# shared builder
pgrep -fl 'vm-supervise.*jailoc-vm/builder'
# isolated builder of a workspace
pgrep -fl 'vm-supervise.*jailoc-vm/workspaces/<workspace>/builder'
```

With no match, move the disk aside and run `jailoc up` (use the isolated path for an isolated builder):

```bash
mv ~/.cache/jailoc-vm/builder/docker.img ~/.cache/jailoc-vm/builder/docker.img.bak-$(date +%Y%m%d-%H%M%S)
jailoc up <workspace>
```

The backup takes host disk space until you delete it, and the next build recreates the cache from scratch, so it takes longer. Delete the backup once the builder works. Image layers in the VM image cache and workspace homes are unaffected.

## State or cache disk is full

**Symptom:** `jailoc up` fails with `no space left on device`, and the error says the guest's agent state or `~/.cache` filesystem is full. It names the disk image, `state.img` or `cache.img`, and the setting that grows it. A failed copy of the previous home directories onto a new `state.img` ends the same way.

jailoc reports a full state or cache disk only when the guest's console shows an out-of-space error about that disk or on a path on it. The guest writes to its own ext4 filesystem there, so the disk is full and not the host: a full host disk fails the guest's writes to the sparse image with I/O errors instead.

**Fix:** raise the setting the error names, then start the workspace again:

```toml
[workspaces.myproject]
state_disk = "20g"   # agent state
cache_disk = "40g"   # ~/.cache
```

```bash
jailoc up myproject
```

The disk and its filesystem grow and keep their content; they never shrink. A failed copy of the previous home directories leaves them where they were, and the next start copies them again. Pick a size the host disk can hold, since the sparse file can grow up to it.

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
