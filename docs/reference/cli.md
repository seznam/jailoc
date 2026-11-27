# CLI Reference

## Synopsis

```text
jailoc [path] [flags]
jailoc [command] [flags]
```

`jl` is available as a shorter alias for `jailoc` when installed via Homebrew or unpacked from the release archive.

## Global Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--workspace` | `-w` | `default` | Target workspace name. Overrides path-based auto-detection where applicable. |
| `--no-color` | | `false` | Disable colored output. |
| `--config` | | `$JAILOC_CONFIG`, else `~/.config/jailoc/config.toml` | Configuration file to use. See [configuration file location](configuration.md#file-location). |

### Workspace resolution

Commands pick the workspace in this order:

1. The `[workspace]` argument, for commands that accept one.
2. The `--workspace` flag.
3. The workspace whose `paths` entry is the longest prefix of the current directory. If several workspaces tie on the deepest match, the command fails and asks for `-w`.
4. `default`.

Exceptions:

- **`--all` (`-A`)**: `up`, `down`, `restart`, `reload` and `status` act on every configured workspace one after another, in name order, and do not take a `[workspace]` argument or `--workspace`. A failure in one workspace does not stop the others; the command reports all failures and exits non-zero.
- **`jailoc add`**: matches the path being added instead of the current directory. With `--workspace`, the added path must lie under one of that workspace's paths.

---

## Commands

### `jailoc`

Open a session of the workspace's coding agent in the workspace VM.

```text
jailoc [path] [flags]
```

- The target directory is `path` (`~` is expanded) or the current working directory, and must be a directory.
- Without `--workspace`, the workspace is resolved from the target directory.
- When the directory is not under any path of the workspace, jailoc asks whether to add it.
- When several workspaces match the directory at the same depth, jailoc fails and asks for `--workspace`.
- If the workspace VM is not running, jailoc starts it as [`jailoc up`](#jailoc-up) does.
- It checks that the agent binary is on `PATH` in the VM and runs the agent (see [Coding agents](configuration.md#coding-agents)) over SSH, in a detachable session whose working directory is the target directory.
- Without flags, it attaches to the session already running in the target directory, or starts one.
- `--session` and `--continue` always start a new session.
- The detach keys (`Ctrl-b`, then `d`, by default) or closing the terminal detach; the agent keeps running. Typing `Ctrl-b` twice sends one `Ctrl-b` to the agent.
- Attaching from a second terminal takes the session over and detaches the first one.

| Flag | Short | Description |
|------|-------|-------------|
| `--new` | | Start a new agent session instead of attaching to the one running in the target directory. |
| `--attach ID` | | Attach to the agent session with this ID (see [`jailoc sessions`](#jailoc-sessions)). |
| `--continue` | `-c` | Start a session that continues the agent's last conversation. |
| `--session ID` | `-s` | Start a session that continues the agent conversation with this ID. |
| `--detach-keys KEYS` | | Key sequence that detaches from the session, for example `ctrl-a,d`. Overrides [`detach_keys`](configuration.md#top-level-fields). Default `ctrl-b,d`. |
| `--version` | `-v` | Print the jailoc version. |

Mutually exclusive flags:

- `--session` with `--continue`
- `--new` with `--attach`
- `--attach` with `--session` or `--continue`

See the [sessions how-to](../how-to/sessions.md).

---

### `jailoc up`

Start a workspace VM.

```text
jailoc up [workspace] [flags]
```

1. Validates secret sources on the host.
2. Builds the workspace image with the host Docker daemon or in a builder VM (see [Image Resolution](image-resolution.md)).
3. Converts its layers into read-only EROFS images in the VM image cache.
4. Boots them as the root file system of the workspace VM behind the host-side network broker.
5. Delivers `env` and secrets to the guest.

When the VM already runs, `jailoc up` does what [`jailoc reload`](#jailoc-reload) does.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Start every configured workspace, in name order. |

---

### `jailoc down`

Stop a workspace VM.

```text
jailoc down [workspace] [flags]
```

- Shuts down the workspace VM and its network broker. All agent sessions end.
- Keeps the persistent home directories (the [agent's state directories](configuration.md#coding-agents) and `~/.cache`).

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Stop every configured workspace. Combined with `--purge`, purges every workspace. |
| `--purge` | | Also delete the workspace state listed below. |

**`--purge` deletes**

- The VM state.
- The persistent home directories.
- The Docker data disk of the workspace VM.
- Any cached `from_command` secrets in the system keychain. The command fails if an entry can't be deleted; the next purge retries it.
- The workspace's [inspection CA](../explanation/network-isolation.md#the-inspection-ca). The next start creates a new one.
- With `isolated_build`, the workspace's builder VM and build cache.

**`--purge` keeps** named agent [profiles](configuration.md#agent-profiles).

---

### `jailoc restart`

Stop and start a workspace VM with the current configuration.

```text
jailoc restart [workspace] [flags]
```

- Equivalent to `jailoc down` followed by `jailoc up`: rebuilds the workspace image and ends running sessions.
- If the workspace is not running, starts it.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Restart every running workspace. Stopped workspaces stay stopped. |

---

### `jailoc reload`

Apply configuration changes to a running workspace VM without restarting it.

```text
jailoc reload [workspace] [flags]
```

**Updated live**

- Network policy: `allowed_hosts`, `allowed_networks`, `network.filtered_dns`, `network.dns_upstream`, `network.dns_blocked_zones`, and turning `network.ipv6` off (turning it on needs `jailoc restart`)
- Published `ports`
- SSH agent: `ssh_auth_sock`, with the socket read again from `$SSH_AUTH_SOCK`
- GPG agent: `gpg_agent`, with the socket looked up again with `gpgconf` and the public keys imported again
- `ca_bundle`
- `snapshots`, including the Git configuration and `known_hosts`: every reload copies the sources again, adds new entries and removes dropped ones
- `env`, `env_file` and secrets
- `timezone`, or the host's current zone when it is unset
- `tmp_ttl`

**Needs a new VM**

`jailoc reload` lists these changes, and [`jailoc restart`](#jailoc-restart) applies them:

- `paths` and `mounts`, including `agents.<name>.mounts`
- the agent's `profile`
- `cpu`, `memory`
- `enable_docker`, the set of guest ports the `proxy` routes use (renaming a route, changing `auth`, or adding one on a port already in the set is applied by `jailoc reload`), `protect_git`
- `tmp_storage`, `tmp_size`
- the working directory (first entry of `paths`, or `/home/agent` when `paths` is empty)
- the jailoc guest, after a jailoc upgrade that changed the protocol between jailoc and the guest

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Reload every running workspace. Stopped workspaces are skipped. |
| `--purge-cache` | | Evict cached `from_command` secrets with a `ttl` from the system keychain and run their commands again. Fails if an entry can't be deleted; the next purge retries it. |

**Caveats**

- Fails when the workspace is not running.
- Running sessions keep running with their old environment; sessions started afterwards get the updated `env`, `env_file` entries and env secrets.
- Files opened before a reload keep their old snapshot content.
- Concurrent reloads of one workspace, including those `jailoc up` starts, wait for each other and run one at a time.
- Changes to `proxy` routes apply on reload. A VM started by a jailoc release that predates live proxy reloads keeps the guest ports of its `proxy` routes reserved until a restart, even when the configuration dropped `proxy`; a reload whose `host_ports` or `ports` use one of them fails and asks for [`jailoc restart`](#jailoc-restart).

---

### `jailoc status`

Show the agent of the workspace (with the package version jailoc installs and extra `args`) and whether its VM is running.

```text
jailoc status [workspace] [flags]
```

**Shown for a running VM**

- Uptime, CPUs, memory and Docker disk.
- The storage mode, configured size, cleanup TTL, and used, free and total capacity of `/tmp` (`Tmp` warns when `/tmp` is 90% full or more, and shows `unknown` when the guest can't be reached).
- Where the agent's persistent home directories live: the named [profile](configuration.md#agent-profiles), `workspace`, or `none` for `profile = false`.
- The number of agent sessions.
- Whether the VM runs the current configuration:
    - `jailoc restart` is needed when a setting that needs a new VM changed (`cpu`, `memory`, `tmp_storage`, `tmp_size`, `agent`, `profile`, `enable_docker`, `protect_git`, `proxy` routes of a VM started by a jailoc release that predates live proxy reloads, or a jailoc upgrade that changed the guest protocol).
    - `jailoc reload` is needed when the configuration file changed after the VM started or was last reloaded.
**Shown for a stopped VM**: when it stopped and the last error.

`Data` is the VM state directory on the host.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Show the status of every configured workspace. |

```text
Workspace: myproject
Backend:   vm
Agent:     claude (@anthropic-ai/claude-code@2.1.318)
State:     running (supervisor pid 4242)
Started:   2026-10-31T09:12:44+01:00 (up 2h5m)
Resources: 2 CPUs, 4096 MiB memory
Profile:   workspace
Tmp:       ram, 1 GiB, cleanup after 8h;
12 MiB of 1 GiB used (1%), 1012 MiB free
Sessions:  2 (1 attached); `jailoc sessions myproject` lists them
Config:    changed since the VM started; `jailoc reload myproject` applies it
Data:      ~/.cache/jailoc-vm/workspaces/myproject
```

---

### `jailoc list`

List every configured workspace.

```bash
jailoc list
```

Alias: `jailoc ls`.

Columns: workspace and its agent, whether its VM is running, the agent sessions of a running VM (and how many have a terminal attached), and its paths. `*` marks the workspace of the current directory.

```text
  WORKSPACE  AGENT      STATE    SESSIONS        PATHS
  default    opencode   running  0               ~/projects
* myproject  claude     running  2 (1 attached)  ~/projects/myproject
  scratch    opencode1  stopped  -               -
```

`SESSIONS` shows `?` when jailoc cannot reach a running VM within 5 seconds.

---

### `jailoc logs`

Print the logs of a workspace.

```text
jailoc logs [workspace] [flags]
```

Prints three logs, in this order:

1. The log of the workspace's last `jailoc up` (`up.log`): its steps with their durations, Docker pulls, builds and build steps, image size and conversion.
2. The VM supervisor log.
3. The guest console log.

When `jailoc up` failed before the VM started, only its log is printed, ending with the error.

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Follow log output until interrupted. |

With `--follow`:

- It keeps printing what is added to all three logs, from a running `jailoc up` and its image build through the VM boot.
- A log that the next `jailoc up` or VM start begins afresh is printed again from its start.

---

### `jailoc traffic`

Print the network activity of a workspace VM from the network broker's log. The log persists after the VM stops.

```text
jailoc traffic [workspace] [flags]
```

The output covers:

- Allowed and denied connections, with the DNS name they were looked up by.
- For allowed TCP connections: the TLS server name and ALPN, or the HTTP method, host and path (without query string).
- DNS answers.
- Names blocked by `network.filtered_dns`.
- Failed dials.
- Connections to published ports.
- Connections relayed to `host_ports`, with their host target.

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Keep printing new events until interrupted. |
| `--lines N` | `-n` | Number of past events to print. Default `100`; `0` prints none, `-1` all. |
| `--json` | | Print the shown events as their JSON log lines, unchanged. |
| `--all` | | Also show DHCP, NIC, SSH agent forwarding, jailoc's own SSH connections and the events of rules with [`log = false`](configuration.md#quiet-rules). |

**Output without `--json`**

- Control characters in logged values are escaped.
- DNS answers are sorted and deduplicated.
- Consecutive addresses that fill exactly one aligned block print as a CIDR prefix (`1.2.3.4/30`).
- IPv4 addresses that differ only in one octet print as a pattern listing the values of that octet (`104.16.{0-11}.34`, `10.0.0.{1,3,5-9}`).
- Both compact forms stand for exactly the answered addresses; `--json` keeps the original list.

**TLS and HTTP inspection**

- TLS connections are identified from the unencrypted ClientHello only. Their content is not decrypted, except for connections covered by a rule with an [`http` table](configuration.md#http-rules).
- For those connections `jailoc traffic` prints one line per request, after the response status is known, or when the request is denied (JSON event `l7`). The line shows protocol (`https` or `http`), method, host, path without query string, status and the deciding rule.
- A denied request adds the reason and has status `403` or `421`. Reasons: `http-no-match`, `bad-path`, `connect-denied`, `host-mismatch`, or `rule <name>`.
- A request the broker could not forward has status `502` and the error. For a failed upstream certificate check a hint says that the broker trusts only the host system roots and `ca_bundle`.

**`inspect-error` lines**

- They report a TLS handshake the guest rejected, or traffic denied after decryption because it isn't HTTP.
- They carry the connection, destination, server name, error and a hint.
- For a rejected handshake the hint says that the client doesn't trust the workspace inspection CA and that removing the `http` table from the rules matching the host passes the connection through undecrypted.
- See [Certificate error on an inspected host](../how-to/troubleshooting.md#certificate-error-on-an-inspected-host).

---

### `jailoc strace`

Print the processes started in a workspace VM.

```text
jailoc strace [workspace] [flags]
```

The output has two kinds of records:

- Started processes: time, PID, parent PID, user, working directory and command line.
- Failed processes: time, PID, exit status (`exit N`) or the signal that killed them, and command line.

The guest records every process start from boot and the exit of each recorded process. The log of the previous boot is kept as `exec.prev.jsonl` in the VM state directory.

| Flag | Short | Description |
|------|-------|-------------|
| `--follow` | `-f` | Keep printing new lines until interrupted. |
| `--lines N` | `-n` | Number of past lines to print. Default `100`; `0` prints none, `-1` all. |
| `--json` | | Print the shown records as JSON lines, unchanged. |
| `--all` | | Also show jailoc's own guest processes and processes that exit with status 0. |

- Without `--json`, control characters in logged values are escaped.
- The record is written inside the VM: root in the VM (`sudo`) can stop or alter it.
- Processes that exit before the guest reads their details show only their name.
- When the guest misses process events during a burst, failures of the processes running at that moment are not recorded.

---

### `jailoc exec`

Run a command as the agent user in a running workspace VM.

```text
jailoc exec [flags] -- COMMAND [ARG...]
```

- The command runs with the session environment and secrets.
- Its working directory is the current directory when a workspace path contains it, otherwise the first workspace path.
- When standard input is a terminal, the command gets one, including window size changes.
- The command's exit status becomes jailoc's exit status.

Flags must come before the command.

---

### `jailoc shell`

Open a login shell as the agent user in a running workspace VM.

```text
jailoc shell [workspace] [flags]
```

- The shell starts in the same directory as `jailoc exec`.
- It is not a detachable session: closing the terminal ends it.

---

### `jailoc auth`

Print or re-open a pending browser login link that a tool in a workspace VM passed to `xdg-open` or `$BROWSER`. Retry and recovery only: jailoc opens the browser on its own, so a normal login needs no command.

```text
jailoc auth [workspace] [flags]
```

- The workspace must be running. The command reads the link from the network broker and doesn't boot the VM.
- The broker keeps a link only when its `redirect_uri` is an explicit `http` loopback URL (`localhost`, `127.0.0.1` or `[::1]`, port 1024-65535) and a guest listener exists on that port. Each link expires after 3 minutes; at most 8 are pending.
- When the tool calls the opener, jailoc binds the host callback listener and opens the exact captured URL in the default browser of the host: `/usr/bin/open` on macOS, `xdg-open` from `PATH` on Linux, without a shell and with the environment that `jailoc up` inherited. The opener in the guest exits 0 only when the host accepted the launch; a window opening or the login completing isn't guaranteed. It exits non-zero when the launch fails, `xdg-open` is unsupported or the URL isn't loopback. After a failed launch of an eligible login, the callback and link stay available for 3 minutes.
- A workspace running the binary of an older jailoc needs `jailoc restart` to get automatic opening. Recovery works only if a pending link is available.
- By default the exact URL is printed to stdout and guidance to stderr. Nothing is launched.
- With `--open`, the URL opens in the default browser of the host with the same fixed opener, in the environment of the invoking terminal. Only its origin, the callback port and the time left are printed.
- With several pending links, the command lists the port, origin and time left of each, without the URL, and fails until `--port` selects one.
- A `ports` entry for the same callback port also opens the browser automatically, but its login isn't listed as pending.

| Flag | Default | Description |
|------|---------|-------------|
| `--open` | `false` | Re-open the link in the default browser of the host instead of printing it. |
| `--port N` | | Select the pending link with this callback port (1-65535). |

The URL is a credential: jailoc keeps it only in memory and doesn't log it or store it on disk. Recognized OAuth queries are redacted in guest exec logs, but the printed output, terminal capture, host browser history and the opener's arguments are outside jailoc's control. The guest picks the origin of the URL that is opened automatically; see the warning in [OAuth link handoff](../explanation/security-boundaries.md#oauth-link-handoff). For the walkthrough and Linux desktop requirements, see [Complete browser login](../how-to/workspace-configuration.md#complete-browser-login) and [Browser login link is missing](../how-to/troubleshooting.md#browser-login-link-is-missing).

---

### `jailoc sessions`

List the agent sessions running in a workspace VM, or stop one.

```text
jailoc sessions [flags]
jailoc sessions stop ID [flags]
```

- Lists each session's ID, state (`attached` or `detached`), start time, directory and command, most recently attached first.
- `stop` ends the session's agent; an attached terminal is disconnected.
- Sessions end when the workspace VM stops.

---

### `jailoc config`

Print the resolved configuration of a workspace.

```text
jailoc config [workspace] [flags]
```

<a id="reads-the-configuration-file-resolves-defaults-agent-settings-and-expansions-for-the-selected-workspace-from-the-argument-workspace-or-the-current-working-directory-and-prints-the-effective-workspace-configuration-together-with-its-computed-paths-config-file-workspace-config-dir-workspace-cache-dir-vm-state-dir-and-profile-dir-secrets-are-listed-under-secrets-as-name-destination-from_source-reference-for-example-gh_token-env-from_env-host_github_token-npm_token-env-from_command-op-read-opdevnpmtoken-ttl-1h0m0s-or-db_cert-file-from_file-homeyouconfigdbpem-only-the-source-reference-is-printed-secret-values-are-never-read-commands-are-never-run"></a>

- Reads the configuration file and resolves `[defaults]`, agent settings and `~` expansions for the selected workspace (from the argument, `--workspace`, or the current working directory).
- Prints the effective workspace configuration together with its `Computed Paths`: `Config File`, `Workspace Config Dir`, `Workspace Cache Dir`, `VM State Dir`, and `Profile Dir`.
- Lists secrets under `Secrets:` as `NAME (<destination> from_<source> <reference>)`, for example `GH_TOKEN (env from_env HOST_GITHUB_TOKEN)`, `NPM_TOKEN (env from_command "op read op://dev/npm/token", ttl 1h0m0s)` or `db_cert (file from_file /home/you/.config/db.pem)`.
- Prints only the source reference: secret values are never read, commands are never run.

| Flag | Short | Description |
|------|-------|-------------|
| `--all` | `-A` | Show the resolved configuration of every configured workspace. |

---

### `jailoc add`

Add a directory to a workspace's `paths` list.

```text
jailoc add [path] [flags]
```

- Appends `path` (default: the current working directory) to `workspaces.<name>.paths` in the configuration file.
- The path must be one `paths` accepts: not a guest-internal directory, the home directory, the host root or a protected path such as `~/.ssh`; see the [configuration reference](configuration.md#paths).
- Secret sources of the target workspace are validated before the configuration changes.
- If the workspace VM is running, it is restarted so the new share takes effect.
- Without `--workspace`, the workspace is resolved from the path being added (longest prefix).
- If `--workspace` is set and the path is not under any configured path of that workspace, `jailoc add` returns an error.

---

### `jailoc setup`

Interactive onboarding and workspace setup wizard (`jailoc wizard` is an alias).

```text
jailoc setup [path] [flags]
jailoc wizard [path] [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--dry-run` | | Print the generated or updated `config.toml` without writing it. |
| `--yes` | `-y` | Accept the default answer to every question. Required when standard input is not a terminal. |

`jailoc setup` operates in two modes depending on the state of `config.toml`:

- **First-time onboarding** (when `config.toml` does not exist or still matches the unmodified default template): asks for `[defaults]` settings and configures the initial workspace for `path` (default: the current working directory). The settings are `agent`, model providers for OpenCode and OmO (`agent_endpoints`), `git_config`, `ssh_auth_sock`, `gpg_agent`, network egress policy `dev-deps` vs `internet` plus optional extra hostnames/CIDRs, `enable_docker`, `cpu` and `memory`, followed by a review summary before writing `config.toml`.
- **Add-workspace mode** (when a customized `config.toml` already exists): asks for a workspace name (default: sanitized basename of `path` or `--workspace`), workspace directory path, optional agent override, Docker inside the VM, workspace network rules, and model providers when `agent_endpoints` is needed, then shows a review summary and patches `[workspaces.<name>]` while preserving existing comments in `config.toml`. Answering `inherit` removes the workspace's own `agent`, `enable_docker` or `network.rules`.
- Interactive prompts run inline in the terminal (`Shift+Tab` moves back to previous fields or steps). Setting `ACCESSIBLE=1` or `TERM=dumb` switches to accessible plain-text prompts.
- A workspace directory that `paths` rejects, such as `/`, the home directory or `~/.ssh`, is refused; see [`paths`](configuration.md#paths).
- When standard input and standard output are terminals and `config.toml` is missing or unmodified default, running `jailoc` or `jailoc up` offers to run `jailoc setup` first.
- `jailoc up <workspace>` uses `<workspace>` as the default workspace name, and the command then starts the workspace the wizard created.

---

### `jailoc migrate`

Update an existing configuration to the current format.

```text
jailoc migrate [flags]
```

| Flag | Short | Description |
|------|-------|-------------|
| `--dry-run` | | Print the planned changes and the resulting `config.toml` without changing anything. |
| `--yes` | `-y` | Accept the default answer to every question. Required when standard input is not a terminal. |

When no scope sets `network.agent_endpoints`, `jailoc migrate` asks which model providers OpenCode and OmO may reach (all selected by default; `--yes` takes all) and adds their endpoints to `[defaults.network]` (when the default agent is one of them) and to each OpenCode or OmO workspace that needs other endpoints; see [`rule=agent`](configuration.md#ruleagent).

`jailoc migrate` asks for optional `internal_zones` (comma- or whitespace-separated DNS zones) when no non-empty `internal_zones` exists in `[defaults]` or any workspace and explicit `allow` rules include hostnames, including wildcard hostnames. The answer is stored in `defaults.network.internal_zones` and applies to every workspace. A blank answer leaves the zones unset; `--yes` skips the question. `--dry-run` asks the question and previews the result without writing it. Names in a zone and its subdomains may resolve to private addresses, but a zone alone grants no access; see [Internal zones](configuration.md#internal-zones).

For configuration changes, data transfer and migration troubleshooting, see [Upgrade an existing installation](../how-to/migrate-from-v1.md).

---

### `jailoc self-update`

Replace the running `jailoc` binary with a release from GitHub.

```text
jailoc self-update [flags]
```

| Flag | Description |
|------|-------------|
| `--check` | Report the release that would be installed and the installation type. Installs nothing. |
| `--version <tag>` | Install the given release tag, including a prerelease or an older release. Bypasses the 24-hour cooldown. |
| `--no-cooldown` | Install the most recent stable release even if it was published less than 24 hours ago. |
| `--force` | Skip the development-build and Homebrew guards. Does not skip verification and does not override the Nix guard. |

Without flags, `jailoc self-update` installs the highest stable release that was published at least 24 hours ago. When a newer stable release is still inside the 24-hour window, the command prints a notice and does not install it. The cooldown delays adoption of a new release; it is not evidence that the release is safe.

**Installation guards.** The command resolves the path of the running executable with symlinks evaluated (the `jl` alias resolves to `jailoc`), and acts on the resolved file:

| Installation | Behavior |
|--------------|----------|
| Path under `/nix/store` | Never written, not even with `--force`. Update through Nix. |
| Homebrew | Refused; run `brew upgrade --cask jailoc`. `--force` skips the guard. |
| Development build (`dev` or `(devel)`) | Refused; pass `--force` or `--version`. |

`--check` reports these installations without writing the executable. It can still write the update-check state file.

**Verification.** Before installing, the command:

1. Requires the archive's SHA-256 in `checksums.txt` to match the asset digest that the GitHub API reports.
2. Looks up that digest's attestation through the GitHub API and requires the exact subject and release workflow metadata to match.
3. Downloads the archive with a size limit and checks its SHA-256 before decompressing it.
4. Runs `gh attestation verify` when `gh` is installed. This checks the signature, timestamp, repository and release workflow cryptographically. Any failure stops installation, including an unsupported `gh` version. Without `gh`, the command warns that the signature was not verified locally.
5. Extracts only the exact `jailoc` file after the preceding checks pass.

The attestation metadata from the GitHub API is what GitHub reports over HTTPS. It is not an independent cryptographic verification; without `gh`, the install relies on trust in GitHub and the TLS connection.

**Replacement.** The new binary is written to a temporary file next to the executable with mode `0755`, synced to disk, and, on macOS, stripped of the `com.apple.quarantine` attribute. It then replaces the executable by rename, which gives the path a fresh inode. The `jl` alias is not rewritten.

!!! note "Running workspaces"
    Running workspace VMs keep using the guest, network broker and supervisor of the old version until you run `jailoc restart <workspace>`.

`jailoc self-update` never runs automatically, and the update notifier does not run during it.

---

### `jailoc completion`

Generate a shell completion script.

```text
jailoc completion bash|fish|powershell|zsh
```

---

## Update Checks

jailoc checks GitHub Releases for newer versions when a command runs. The check runs in the background and does not block the command.

- **Cache**: the HTTP response is cached for 24 hours. The cache stores the candidate releases with their publication timestamps, and jailoc evaluates the cooldown locally on each run. A state file in an older format is discarded and fetched again.
- **Notice**: when a stable release at least 24 hours old is newer than the running version, a notice is printed to stderr. A newer release inside the 24-hour window produces no update notice, and a later run reports it once it ages out. The check runs only when stderr is a TTY; otherwise it is skipped entirely and nothing is printed.
- **Color**: respects `--no-color`.
- **Install**: the notice does not install anything; run [`jailoc self-update`](#jailoc-self-update).

Update checks are skipped when:

- the build version is `dev` or `(devel)` (local `go build` without release ldflags),
- the `CI` environment variable is set to any non-empty value,
- the `JAILOC_NO_UPDATE_NOTIFIER` environment variable is set to any non-empty value,
- the command is `jailoc self-update`.

| Variable | Default | Description |
|----------|---------|-------------|
| `JAILOC_NO_UPDATE_NOTIFIER` | (unset) | Set to any non-empty value to disable update checks. |
