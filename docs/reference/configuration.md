# Configuration Reference

Every `config.toml` field with its type, default, validation rule and merge behavior.

## File location

| Source | Precedence |
|--------|------------|
| `--config <file>` | 1 |
| `JAILOC_CONFIG=<file>` | 2 |
| `~/.config/jailoc/config.toml` | 3 (default) |

- `~` is expanded and relative paths are resolved against the current directory.
- The default file is auto-created with defaults on first run. A file set with `--config` or `JAILOC_CONFIG` must exist.
- Per-workspace files, such as the CA bundle copied by [`ca_bundle`](#ca_bundle), live in `workspaces/<name>/` next to the configuration file.
- VM state under `~/.cache/jailoc-vm/` is keyed by workspace name only and is shared by all configuration files: workspaces with the same name in two files control the same VM.
- All fields are optional unless noted.
- jailoc ignores settings it does not know and prints a warning naming each of them. For a config-load error naming a rejected setting, see [Upgrade an existing installation](../how-to/migrate-from-v1.md#recognize-a-v1-configuration).
- When jailoc prints deprecation notices, it recommends `jailoc migrate` after the notices to update deprecated settings in `config.toml`.

---

## Top-level fields

Besides the tables `[vm]`, `[base]`, `[defaults]` and `[workspaces.<name>]`, the configuration has one setting at the root level. Put it before the first table.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `detach_keys` | string | `"ctrl-b,d"` | Key sequence that detaches the terminal from an agent session. The `--detach-keys` flag of [`jailoc`](cli.md#jailoc) overrides it. |

Syntax of `detach_keys`:

- A comma-separated list of keys, typed one after another, in the format of `docker attach --detach-keys`.
- A key is one of:
    - `ctrl-<letter>` (lowercase)
    - `ctrl-@`, `ctrl-[`, `ctrl-\`, `ctrl-]`, `ctrl-^`, `ctrl-_`
    - `DEL`
    - a single character other than a comma

Behavior while a session is attached:

- jailoc holds back input that starts like the sequence until the next key decides.
- Typing the first key twice sends it once to the agent, as in tmux.
- Keys match also when the agent switches the terminal to an extended keyboard mode (xterm modifyOtherKeys or the kitty keyboard protocol). Key release and repeat events, presses of a modifier key alone, and Caps Lock or Num Lock do not break the sequence.

```toml
detach_keys = "ctrl-a,d"
```

---

## `[vm]`

Host runtime of the workspace VMs. All fields are optional.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runner` | string | (embedded or package-provided) | Path to a `jailoc-vm-runner` (or `go-microvm-runner`) binary. Must be absolute; supports `~` expansion. |
| `lib_dir` | string | (none) | Directory containing `libkrun` and `libkrunfw` for a configured `runner`. Must be absolute; supports `~` expansion. |
| `builder` | string | `"auto"` | Where workspace images are built: `"auto"`, `"docker"` or `"vm"`. |
| `builder_disk` | string | `"20g"` | Size of the data disk of the shared builder VM, at least `1g` (checked when the shared builder VM starts, not by validation). The disk can grow but not shrink. |

**`runner`: runtime selection order**

1. A configured `runner`.
2. A runtime provided by the package (for example, the Nix package on Linux, which references its runner and libraries in the Nix store).
3. The VM runtime (`jailoc-vm-runner`, `libkrun`, `libkrunfw`) embedded in release builds for darwin/arm64, linux/amd64 and linux/arm64, unpacked into `~/.cache/jailoc-vm/runtime/` on first use.

Builds with none of these (other platforms, builds from source) require `runner` and `lib_dir`.

**`lib_dir`: when it applies**

- Ignored when `runner` is empty, including with a package-provided runtime, which always uses its own library directory.
- When empty with a configured `runner`, jailoc uses `/opt/homebrew/lib` on macOS if it exists, otherwise the system library path. This fallback applies only to a configured `runner`.

**`builder`: values**

| Value | Where the image is built |
|-------|--------------------------|
| `"auto"` | The host Docker daemon when it answers and runs Linux on the host's CPU architecture, otherwise the shared builder VM. |
| `"docker"` | The host Docker daemon; it is required. |
| `"vm"` | The shared builder VM, always. |

A workspace with `isolated_build = true` always uses its own builder VM, whose data disk follows the workspace's [`docker_disk`](#vm-resource-and-docker-fields), not `builder_disk`.

**`builder_disk`: details**

- It sizes the data disk (`docker.img`) of the shared builder VM in `~/.cache/jailoc-vm/builder`. The format and limits are in [`cpu`, `memory` and `docker_disk`](#cpu-memory-and-docker_disk).
- A value larger than the existing disk grows it, and the file system grows with it, keeping its content. A smaller value makes `jailoc up` fail; the disk never shrinks.
- jailoc maintains the cache of a builder VM it owns, and never prunes the host Docker daemon. See [Builder cache maintenance](image-resolution.md#builder-cache-maintenance).
- A builder whose disk is already full can't prune itself. See [Builder VM disk is full](../how-to/troubleshooting.md#builder-vm-disk-is-full).

### Example

```toml
[vm]
runner = "~/.local/bin/go-microvm-runner"
lib_dir = "/opt/homebrew/lib"
builder_disk = "40g"
```

---

## `[base]`

Global base image settings. Controls the base image used when no workspace or default `image` is set. See [Image Resolution](image-resolution.md).

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to a Dockerfile for the base image. |
| `ubuntu_mirror` | string | `"http://cz.archive.ubuntu.com/ubuntu"` | HTTP URL of the Ubuntu archive the embedded images install apt packages from. |

`dockerfile` details:

- When set, it takes priority over the embedded Dockerfile.
- Build failure is fatal.
- Maximum file size for HTTP sources: 1 MiB.

`ubuntu_mirror` details:

- Passed as the `UBUNTU_MIRROR` build argument to the embedded Dockerfile and the Docker layer (`enable_docker`). It replaces `archive.ubuntu.com`, `security.ubuntu.com` and `ports.ubuntu.com` in `/etc/apt/sources.list.d/ubuntu.sources`.
- The embedded base image keeps the replaced sources, so `apt-get install` in the workspace VM uses the mirror too.
- Images from `[base] dockerfile` and `image` keep their own apt sources; workspace Dockerfiles built on the embedded base image inherit the mirror.
- The mirror must carry the packages of the host architecture (arm64 or amd64) for the `resolute`, `resolute-updates`, `resolute-backports` and `resolute-security` suites.
- Validation: `http` scheme, non-empty host, no query, fragment, whitespace, quotes, `&` or `\`.
- `https` is rejected: the embedded images have no CA certificates before their first `apt-get install`. apt verifies the downloaded indexes and packages with the archive's GPG signatures.
- `"http://archive.ubuntu.com/ubuntu"` installs from the main Ubuntu archive, security updates included.

### Example

```toml
[base]
dockerfile = "https://example.com/Dockerfiles/custom"
```

```toml
[base]
ubuntu_mirror = "http://de.archive.ubuntu.com/ubuntu"
```

---

## `[defaults]`

Defaults applied to all workspaces. A workspace can override or extend them; see [`[workspaces.<name>]`](#workspacesname) and [Merge Semantics](#merge-semantics).

### Agent and image fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend` | string | `"vm"` | Accepted values: `"vm"` or empty. All other values are rejected. |
| `agent` | string | `"opencode"` | Coding agent of the workspaces. Values below. See [Coding agents](#coding-agents). |
| `agents` | table | `{}` | Settings per coding agent and custom agent definitions, keyed by agent name. See [`agents`](#agents). |
| `image` | string | (none) | Pre-built image used as the base for workspace Dockerfile builds, or used directly when no workspace `dockerfile` is set. |

`agent` values: `"opencode"`, `"opencode1"`, `"opencode2"`, `"claude"`, `"copilot"`, `"omo"`, `"agy"` or a [custom agent](#custom-agents).

### Environment, mounts and snapshots fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `env` | string[] | `[]` | Environment variables for every session, in `KEY=VALUE` format. |
| `env_file` | string[] | `[]` | `.env` files loaded for all workspaces. Each file must exist at config load time. |
| `mounts` | string[] | `[]` | Additional directory shares, `host:container[:mode]`. See [`mounts`](#mounts). |
| `snapshots` | string[] | `[]` | Read-only snapshots of host files or directories. See [`snapshots`](#snapshots). |
| `secrets` | table | `{}` | Secrets for all workspaces. See [`secrets`](#secrets). |

### Network and port fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `network` | table | `{}` | IPv6 and DNS settings of the network broker. See [`[defaults.network]`](#defaultsnetwork). |
| `allowed_hosts` | string[] | `[]` | Hostnames whose A and AAAA records are allowed through the network broker even when they resolve to private addresses. Deprecated: its hostnames become an allow rule and [internal zones](#internal-zones). |
| `allowed_networks` | string[] | `[]` | IPv4 and IPv6 CIDR ranges allowed through the network broker. |
| `ports` | (int \| string)[] | `[]` | Guest ports published on host `127.0.0.1`. See [`ports`](#ports). |
| `host_ports` | (int \| string)[] | `[]` | Host `127.0.0.1` ports or Unix sockets reachable from the guest. See [`host_ports`](#host_ports). |
| `ca_bundle` | bool \| string | `true` | CA bundle added to the VM's system trust store. See [`ca_bundle`](#ca_bundle). |

### Host integration fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `ssh_auth_sock` | bool | `false` | Relay the host SSH agent (`$SSH_AUTH_SOCK`) into the VM at `/run/ssh-agent.sock` and copy `~/.ssh/known_hosts` into it. |
| `gpg_agent` | bool | `false` | Relay the host gpg-agent extra socket into the VM. Requires `gpg` in the image. |
| `git_config` | bool | `true` | Copy the host Git configuration read-only into the VM. See [`git_config`](#git_config). |
| `import_agent_config` | bool | `true` | Copy the coding agent's host configuration read-only into the VM. See [`import_agent_config`](#import_agent_config). |
| `timezone` | string | (host zone) | Time zone of the workspace VM, an IANA name such as `"UTC"` or `"Europe/Prague"`. Omitted: the host's local zone. See [`timezone`](#timezone). |
| `notifications` | string | `"auto"` | How an attached OpenCode session tells you that the agent finished or needs input while its terminal is not focused: `"auto"`, `"native"`, `"osc"` or `"off"`. See [`notifications`](#notifications). |

`gpg_agent` details:

- The relay socket is `/run/user/<uid>/gnupg/S.gpg-agent` in the VM.
- jailoc imports into the VM keyring the public key of `user.signingKey`, or the public keys of all host secret keys when `user.signingKey` is unset.

`git_config` copies these host files: `~/.gitconfig`, and `config`, `ignore` and `attributes` from `$XDG_CONFIG_HOME/git` (default `~/.config/git`), plus the files they reference under the home directory.

`import_agent_config` copies, for example, `~/.claude/CLAUDE.md` or `~/.config/opencode`.

### VM resource and Docker fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpu` | float | `2.0` | vCPUs of the workspace VM, rounded up. |
| `memory` | string | `"4g"` | Memory of the workspace VM, at least `512m`. |
| `docker_disk` | string | `"20g"` | Size of the Docker data disk of an `enable_docker` workspace and of its `isolated_build` builder VM, at least `1g`. The disk can grow but not shrink. |
| `tmp_storage` | string | `"ram"` | Backing of the guest `/tmp`: `"ram"` or `"disk"`. Needs `jailoc restart`. See [`tmp_storage`, `tmp_size` and `tmp_ttl`](#tmp_storage-tmp_size-and-tmp_ttl). |
| `tmp_size` | string | `"1g"` | Capacity of `/tmp`, at least `16m` with `tmp_storage = "disk"`. Needs `jailoc restart`. |
| `tmp_ttl` | string | `"8h"` | Age after which `/tmp` entries are removed, or `"0"` to keep them. Applied by `jailoc reload`. |
| `enable_docker` | bool | `false` | Run a Docker daemon inside the workspace VM. See [`enable_docker`](#enable_docker). |

### Git protection and build fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `protect_git` | bool | `false` | Make Git hooks and configuration read-only inside the VM, and disable `sudo` and Docker there. See [`protect_git`](#protect_git). |
| `isolated_build` | bool | `false` | Build the workspace image in a builder VM of its own. |

`protect_git = true` together with `enable_docker = true` is rejected. The protected files, the repositories searched and the failure conditions are in [`protect_git`](#protect_git).

`isolated_build` details:

- The workspace image is built in a builder VM of its own, with its own build cache and the workspace's network policy, instead of the host Docker daemon or the shared builder VM.
- The build does not inspect HTTPS: `allow` rules with an [`http` table](#http-rules) open their hosts and ports for all methods and paths, and `deny` rules with `http` don't apply.

### Example

```toml
[defaults]
agent = "claude"
image = "myregistry.example.com/myteam/agent-base:v1.2.3"
env = ["GOPRIVATE=*.example.com"]
env_file = ["~/.config/jailoc/shared.env"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8", "fd12:3456::/48"]
ssh_auth_sock = true
cpu = 4.0
memory = "8g"

[defaults.agents.claude]
args = ["--model", "sonnet"]
```

---

## `[defaults.network]`

Network settings of the workspace VM and its network broker. `[workspaces.<name>.network]` takes the same fields; a set workspace field wins.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `ipv6` | bool | `false` | Give the guest IPv6 (`fd00:127::2/64`) and let the broker allow IPv6 destinations by the [IPv6 policy](../explanation/network-isolation.md#ipv6). |
| `dns` | string | (host resolver) | Routable numeric IPv4 resolver used for permitted DNS queries. |
| `rules` | table[] | `[]` | Allow and deny egress rules (`[[<scope>.network.rules]]`). Combined across `[defaults]` and the workspace; `deny` always wins over `allow`. |
| `internal_zones` | string[] | `[]` | DNS zones whose names may resolve to private addresses. Combined across `[defaults]` and the workspace. See [Internal zones](#internal-zones). |
| `agent_endpoints` | string[] | (unset) | Hosts of the [`agent` rule](#ruleagent) for OpenCode and OmO workspaces: the model and auth endpoints the agent may reach. Unset or `[]`: no `agent` rule for them. A set workspace list replaces the defaults list. Ignored for Claude Code, Copilot CLI and Antigravity. |

`ipv6` details:

- On: the guest has IPv6 and the broker applies the IPv6 policy.
- Off: the guest has IPv4 only and the broker denies every IPv6 flow.
- Changing it needs `jailoc restart`. `jailoc reload` turns it off at the broker right away, except in a VM started by a jailoc release without this setting: that VM keeps IPv6 until `jailoc restart`.

`agent_endpoints` details:

- Entries are host names or `*.domain` wildcards; IP addresses, CIDRs and `*` are rejected.
- Applies to the multi-provider agents `opencode`, `opencode1`, `opencode2` and `omo` (and custom agents built `from` them). `claude`, `copilot` and `agy` always get their built-in endpoints; narrow them with a `deny` rule.
- New configurations from the setup wizard and `jailoc migrate` set it to the OpenCode endpoints (see [`rule=agent`](#ruleagent)). Adding a workspace whose agent, set or inherited, is OpenCode or OmO with the wizard writes the workspace list only when neither the workspace nor `[defaults.network]` sets one, and keeps an existing list. Keep only the providers you use, for example `agent_endpoints = ["api.anthropic.com"]`; the other endpoints then show as denied in `jailoc traffic`.
- Unset for a workspace whose agent is OpenCode or OmO: there is no `agent` rule, so the agent reaches its model and auth endpoints only through your other allow rules, and the config load prints a warning.

`internal_zones` details: each zone covers itself and every name under it (`corp.example` covers `gitlab.corp.example`; a leading `*.` is dropped).

### `[[<scope>.network.rules]]`

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `allow` | string[] | (none) | Hostnames, `*.wildcard` names, IP addresses, CIDRs, or `"*"` (public internet). Exactly one of `allow` or `deny` is required. |
| `deny` | string[] | (none) | Hostnames, `*.wildcard` names, IP addresses, CIDRs, or `"*"`. Exactly one of `allow` or `deny` is required. |
| `proto` | string | `"tcp"` | `"tcp"`, `"udp"`, or `"any"`. |
| `ports` | (int \| string)[] | (all) | Port numbers (`1` to `65535`) or `"a-b"` port ranges. |
| `name` | string | `""` | Label shown in `jailoc traffic` (`rule=<name>`) and validation errors. |
| `http` | table | (none) | Inspect HTTP requests to the matched destinations and apply `methods` and `paths` per request. Allowed on `allow` and `deny` rules; requires `proto = "tcp"`. See [HTTP rules](#http-rules). |
| `log` | bool | `true` | `false` hides the events this rule decides from `jailoc traffic` unless `--all` is given. Valid on `allow` and `deny` rules, with or without `http`. See [Quiet rules](#quiet-rules). |

#### Quiet rules

`log = false` only changes what `jailoc traffic` shows: the broker still enforces the rule and records every event in its log, marked `"quiet": true`. `jailoc traffic --all` and `--json --all` show them.

The mark covers the events the rule itself decides:

- the connection (allowed or denied by the rule) and the TLS or HTTP line of that connection;
- a request an `http` rule allows or denies, and a request allowed through a connection the rule allowed.

Events that stay visible:

- Denials for any other reason, such as `http-no-match`, `bad-path`, `host-mismatch`, `sni-required` or `default-deny`.
- Failures: `inspect-error`, `502` upstream errors, and DNS events.
- A request that a different rule denies, on a connection allowed by a quiet rule.

A running workspace needs `jailoc restart` instead of `jailoc reload` to apply the first `log = false` rule when its network broker was started by an older jailoc.

### HTTP rules

A rule with an `http` table decides per HTTP request instead of per connection. The broker decrypts TLS for the flows such a rule covers (see [TLS inspection](../explanation/network-isolation.md#tls-inspection)). Rules without `http` are plain rules.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `http.methods` | string[] | (any) | Request methods. `"*"` matches any method. Values are normalized to upper case and must consist of RFC 9110 token characters. |
| `http.paths` | string[] | (any) | Request path patterns, matched case-sensitively against the path without the query string. |

- `http` takes any target an `allow` or `deny` rule takes: names, `*.wildcard`, `"*"`, IP addresses and CIDRs. Without `ports` it covers all ports.
- `http` requires `proto = "tcp"` (the default). `proto = "udp"` or `"any"` with `http` fails config load.
- An empty table (`http = {}`) is valid and matches every request.

```toml
[[workspaces.api.network.rules]]
name = "github-readonly"
allow = ["api.github.com"]
http.methods = ["GET", "HEAD"]
http.paths = ["/repos/seznam/*"]

[[workspaces.api.network.rules]]
name = "no-writes"
deny = ["registry.example.com"]
http.methods = ["DELETE", "POST", "PUT", "PATCH"]
```

#### Path patterns

A pattern starts with `/` and is split into segments at `/`.

| Segment | Matches |
|---------|---------|
| literal | exactly that segment |
| `*` | exactly one non-empty segment |
| `**` | zero or more remaining segments; allowed only as the last segment |

Examples:

- `/repos/*` matches `/repos/a`. It does not match `/repos`, `/repos/` or `/repos/a/b`.
- `/repos/**` matches `/repos`, `/repos/`, `/repos/a` and `/repos/a/b`.
- A trailing `/` is a trailing empty segment and matches literally.

A pattern is rejected at config load when it:

- does not start with `/`;
- has a partial wildcard (`foo*`);
- has `**` anywhere but last;
- has an empty segment in the middle (`//`);
- has a `.` or `..` segment;
- contains `?`, `#`, `%`, `\` or a control character.

A method with a character outside the RFC 9110 token set and `http` with a `proto` other than `tcp` are rejected as well.

#### Request path checking

**When the path is checked.** The broker checks and matches the path only when a path pattern takes part in the decision: some `http` rule in the connection's scope (allow or deny) lists `paths` and covers the request's method. Otherwise the path is not evaluated, and an odd path such as `//x` or `/a%5Cb` is never denied as `bad-path`.

**How patterns match.** When patterns apply, the broker matches them against the percent-decoded segments of the request path, taken between literal `/`. An encoded slash does not split a segment: `*` matches `@scope%2Fname` (decoded `@scope/name`) as one segment, a literal never does, because a pattern segment cannot contain `/`.

**`bad-path` denial.** A request is denied with reason `bad-path` when its path:

- does not start with `/`, or contains `//` or `\`;
- contains an encoded backslash or NUL (`%5c`, `%00`, any case);
- has a segment that fails percent-decoding, or contains a control character;
- has a segment that is, or after splitting on its encoded slashes contains, a `.` or `..` piece (`..`, `%2e%2e`, `x%2F..%2Fadmin`) or an empty piece (`%2Fadmin`, `a%2F`, `a%2F%2Fb`).

**`deny` and `allow` see different paths.**

| Rule | Path the pattern sees |
|------|-----------------------|
| `deny` | The raw segments, and also the path with encoded slashes decoded and split again, so `/a/b` denies `/a%2Fb` as well as `/a/b`. |
| `allow` | The raw segments only. |

!!! warning
    A server that decodes `%2F` before routing sees more segments than an `allow` pattern's `*` matched. Use `deny` rules for paths that must stay unreachable.

#### Evaluation

**`deny` rules with `http`.** They never deny a connection and never block DNS; they act on requests only. For every other decision step the rule counts like a plain rule: an `allow` rule with `http` lets the connection reach the TLS or HTTP stage, a plain `deny` rule denies it first.

**Inspection scope.** The broker inspects a TCP connection when an `http` rule (allow or deny) matches its port and, after the TLS server name or HTTP `Host` is known, its name or address. These rules form the http scope of the connection: the `http` rules that match the server name together with those that match the destination address. An empty scope leaves the connection to plain rules and undecrypted.

**Connections without a name** (TLS without SNI, HTTP/1.0 without `Host`):

- They are judged by the `http` rules that match their destination address (an IP or CIDR target, or `*`). Without such a rule the connection is handled as if no `http` rule existed.
- With such a rule the connection is inspected under the destination's IP address as its name:
    - the broker's certificate carries the IP as a subject alternative name;
    - the upstream certificate must be valid for the IP;
    - every request's `Host` must be the IP (an HTTP/1.0 request without `Host` counts as such).
- A connection without a name whose TLS offers no HTTP protocol in ALPN is handled like one with a name (see [Protocol detection](#protocol-detection)): the plain rules decide, and it is inspected under the IP only when an `http` rule matches the address.
- IPv6 literals in `Host` or `:authority` are compared by address, so the compressed and expanded forms are the same host.
- Connections that name rules require a name for (a connection allowed by a name, a plain `deny` rule with names) still fail with `sni-required` without one.

**Connections that were not inspected.** They stay checked: if a later request on the connection names a host that an `http` rule covers, the request is refused with reason `inspection-required`.

**`http` allow rules narrow their host.** When the scope has `http` allow rules, only they decide the requests, and plain `allow` rules matching the same host and port grant nothing for it.

**Order of checks for each request:**

1. The request host (`Host` or `:authority`, port stripped) must equal the connection's server name, otherwise `421` with reason `host-mismatch`.
2. `CONNECT` and HTTP/2 extended `CONNECT` are denied with reason `connect-denied`.
3. When a path pattern applies to the method (see [request path checking](#request-path-checking)), a bad path is denied with reason `bad-path`.
4. A matching `http` deny rule (method and path both match) denies the request with `rule=<name>`.
5. If the scope has `http` allow rules, one must match both method and path, otherwise the request is denied with reason `http-no-match`. If the scope has none, a plain allow rule allowed the connection and the request passes.

!!! warning
    The `http` allow rules of the scope are a union, whether they match by name or by address, and any one of them can allow a request. An `http` allow rule on an IP or CIDR (for example `GET` only) is not a ceiling when the connection presents a name that another `http` allow rule (for example `POST`) matches: the address authorizes the connection, so the presented name doesn't have to resolve to the destination, and the request can use the named rule's methods and paths. Use an `http` `deny` rule, which is checked before the allow rules, as a hard cap.

#### Responses and reasons

| Outcome | Status | Body |
|---------|--------|------|
| Denied request | `403 Forbidden` | `blocked by network policy (rule=<name>)` for a deny rule, `blocked by network policy (reason=<reason>)` otherwise |
| Host does not match the server name | `421 Misdirected Request` | `blocked by network policy (reason=host-mismatch)` |
| Upstream failure (connect or certificate verification) | `502 Bad Gateway` | the error |

| Reason | Meaning |
|--------|---------|
| `http-no-match` | The scope has `http` allow rules and none matched the method and path. |
| `bad-path` | The request path failed [checking](#request-path-checking). |
| `connect-denied` | The request is a `CONNECT` or an HTTP/2 extended `CONNECT`. |
| `host-mismatch` | The request host differs from the TLS server name or `Host` of the connection. |
| `inspection-required` | A request on an uninspected connection names a host that an `http` rule covers. |

#### Protocol detection

Protocol detection decides per connection whether HTTP is served.

**TLS, ClientHello ALPN offers `h2` or `http/1.1`**: decrypted, HTTP served.

**TLS, ALPN offered but neither is HTTP**:

- Plain rules decide; an `http` rule alone never opens it.
- Allowed and covered by an `http` rule (by name, or by address when there is no name): decrypted, offering the guest's ALPN list, and handled like TLS without ALPN. HTTP inside is checked by the `http` rules; anything else is relayed to a verified upstream connection that is offered the negotiated protocol, so the client must trust the inspection CA.
- Allowed and covered by no `http` rule: not decrypted, spliced.

**TLS, no ALPN**: decrypted; the first line decides.

- A request line (`method SP target SP HTTP/x.y`, any method token in any case) or the HTTP/2 preface is served as HTTP.
- Anything else is decided by plain rules and relayed (denied: closed, `inspect-error`).
- A first line that looks like a request but is not valid closes the connection with reason `malformed-request-line`, also when it comes after the 1 s peek. Not valid means: a method followed by two spaces or any other whitespace, leading whitespace or empty lines, trailing whitespace after the version, any line ending in `HTTP/x.y`.
- A first line that starts with a word and whitespace is read to its line end before it is judged, because a lenient server accepts any whitespace as a separator.
- A first line over 8 KiB that is no request line, or an undecided partial line when the 1 s peek ends, closes the connection.
- If the guest sends nothing for 1 s the connection is relayed (server-first protocols), but a request line or preface the guest sends later closes it before anything is forwarded.

**Plain HTTP request**: checked per request, no TLS. A request line that is not valid but looks like one (any whitespace separator, leading empty lines, extra words) is denied, not relayed.

**Cleartext HTTP/2 (prior knowledge, `h2c`)**: served per request.

- Each `:authority` is decided like a server name (it must resolve to the destination, or be its IP), then the `http` scope of that name applies.
- A request without `:authority` is denied (`sni-required`).
- Requests are forwarded upstream as prior-knowledge HTTP/2, so the upstream must speak `h2c`.

**No server name or Host (silent, server-first or other protocols)**: plain rules decide; `http` rules never open non-HTTP traffic; denied connections are reset.

**WebSocket and `h2c` upgrades**:

- A WebSocket upgrade request is checked like any request. After the `101` response the connection passes through unfiltered; frames are not inspected.
- An `Upgrade: h2c` offer is removed from the request, which is then sent as plain HTTP/1.1, so a connection can't be switched to HTTP/2 past the per-request checks.

#### Request bodies and upstream connections

**Request bodies**:

- Bodies with a known length of up to 1 MiB are buffered in memory (64 MiB in total across a workspace's flows) so the broker can resend them on a new upstream connection, for example after an HTTP/2 `GOAWAY`.
- Larger and chunked bodies stream through, and so do HTTP/1.1 bodies sent with `Expect: 100-continue`, which wait for the upstream's `100 Continue`.
- For HTTP/2 guests the broker answers `100 Continue` itself and does not pass `Expect` upstream.

**Upstream certificate and proxies**:

- The broker verifies the upstream certificate against the host's system roots plus the file named by [`ca_bundle`](#ca_bundle) when it is a path. A failure ends the request with `502`.
- The broker does not use `HTTP_PROXY` or another upstream proxy.

!!! note
    Inspection adds the workspace's inspection CA to the guest trust stores, also with `ca_bundle = false`. See [TLS inspection](../explanation/network-isolation.md#tls-inspection).

### Implicit rules and built-in broker behavior

In addition to `[[defaults.network.rules]]` and `[[workspaces.<name>.network.rules]]`, the broker applies the rules below.

#### `rule=agent`

Allow, `proto = "tcp"`, `ports = [443]`, to the coding agent's model and auth endpoints. A matching `deny` rule still overrides it.

- `opencode`, `opencode1`, `opencode2`, `omo`: the hosts in [`agent_endpoints`](#defaultsnetwork); added only when the list is non-empty. The setup wizard and `jailoc migrate` write the list below.
- `claude`, `copilot`, `agy`: the built-in endpoints below; `agent_endpoints` does not apply.

Endpoints per `agent`:

- `opencode`, `opencode1`, `opencode2`, `omo`:
    - `api.anthropic.com`
    - `api.openai.com`
    - `auth.openai.com`
    - `chatgpt.com`
    - `generativelanguage.googleapis.com`
    - `oauth2.googleapis.com`
    - `api.githubcopilot.com`
    - `*.githubcopilot.com`
    - `openrouter.ai`
    - `opencode.ai`
    - `*.opencode.ai`
    - `models.dev`
- `claude`:
    - `api.anthropic.com`
    - `claude.ai`
    - `console.anthropic.com`
    - `platform.claude.com`
- `copilot`:
    - `api.github.com`
    - `github.com`
    - `api.githubcopilot.com`
    - `*.githubcopilot.com`
    - `copilot-proxy.githubusercontent.com`
- `agy`:
    - `generativelanguage.googleapis.com`
    - `cloudcode-pa.googleapis.com`
    - `oauth2.googleapis.com`
    - `accounts.google.com`

#### Other built-in behavior

- **`rule=legacy`** (`proto = "any"`): synthesized in memory when deprecated `allowed_hosts`, `allowed_networks` or `dns_blocked_zones` are set.
- **DoT and QUIC exclusion on `"*"`**: a wildcard rule (`allow = ["*"]`) without explicit `ports` excludes
    - TCP port `853` (DNS over TLS), and
    - UDP port `443` (QUIC/HTTP/3),

    so DNS stays on the gateway resolver and HTTPS clients fall back to TCP port `443` (where TLS SNI is inspected whenever hostname rules or hostname `deny` rules apply on that port). Listing those ports in `ports` opens them.
- **Direct DNS block**: TCP and UDP port `53` to any address other than the gateway (`192.168.127.1`) is denied (`reason=dns-filtered`).
    - An `allow` rule opens it only when it lists port `53` in `ports` and matches the resolver by IP, CIDR, hostname or `"*"` (public resolvers only). A rule without `ports`, including `allow = ["*"]`, never opens it.
    - The broker answers allowed direct queries itself: it sends each query only to that resolver, and applies the same name filtering, NXDOMAIN and `HTTPS`/`SVCB` handling as the gateway resolver.
    - It does not add the answers to its IP-to-name cache, so connections to the returned addresses match hostname rules only through names the gateway resolver returned.
    - `jailoc traffic` shows these queries with `@<resolver>`.
- **Gateway services (`192.168.127.1`)**: the broker serves
    - DHCP,
    - DNS (port `53`),
    - SNTP clock sync (UDP port `123`),
    - SSH/GPG agent relays (`ssh_auth_sock`, `gpg_agent`),
    - configured `host_ports` on the gateway.

    All other connections to the gateway, guest subnet (`192.168.127.0/24`, `fd00:127::/64`), host interface addresses, and hard-denied ranges are always denied.
- **Default `dev-deps` rule in new configs**: when jailoc creates a missing `~/.config/jailoc/config.toml`, it writes an explicit `[[defaults.network.rules]]` entry named `dev-deps` into the file. The rule allows GitHub, GitLab.com, npm, Yarn, Node.js, Bun, PyPI, Astral, Go proxy, crates.io, Rustup, Docker Hub, GHCR, Quay, GCR, MCR, Ubuntu/Debian/Alpine mirrors, and Nix caches. Removing or editing that block in `config.toml` removes or changes those rules.

### Internal zones

A hostname rule opens a private address (RFC 1918, CGNAT, link-local, IPv6 unique local and the other [denied ranges](../explanation/network-isolation.md)) only for names in `internal_zones`. A zone only marks whose DNS answers are trusted; it opens nothing by itself, so the names still need an `allow` rule.

- **DNS**: the broker drops `A` and `AAAA` records with a private address from an answer unless the queried name, the record owner and every `CNAME` in the answer are in an internal zone.
    - A public name with a `CNAME` into an internal zone, or a chain that passes through a public name, is dropped too.
    - Dropped addresses never reach the guest or the IP-to-name cache, and `jailoc traffic` shows them as `dropped <address>: private address outside internal_zones`.
- **Connect time**: a hostname rule matches a private destination only through a cached name in an internal zone. Otherwise the flow is denied with `reason=not-internal-zone` and the name of the rule that matched. The check covers the whole DNS chain of the cached name and uses the current policy, so removing a zone with `jailoc reload` closes its cached names at once.
- **Other rules**: IP and CIDR rules are unaffected, `deny` rules still win, and `"*"` never opens private addresses. Loopback and other hard-denied answers are left in DNS answers; the broker never connects to them.
- **Validation**: a zone must be a domain name.
    - Rejected: `"*"`; top-level domains and other public suffixes (`com`, `co.uk`, `github.io`); wildcard DNS services that resolve names to any address (`nip.io`, `sslip.io`, `xip.io`, `localtest.me`, `lvh.me`, `traefik.me`, `vcap.me`, `local.gd`).
    - Allowed: top-level names without a public registry, such as `internal`, `corp`, `lan`, and `home.arpa`.
- **Legacy `allowed_hosts`**:
    - Its hostnames are trusted as internal zones in memory, and `jailoc migrate` writes them to `internal_zones`, so they keep resolving to private addresses.
    - jailoc does not resolve these hostnames on the host into CIDR rules, so they reach private addresses only through the broker's DNS, which asks the upstream resolvers and doesn't read the host's `/etc/hosts`.
    - A hostname that is a `CNAME` alias of a name outside the internal zones and resolves to a private address does not resolve in the VM. `jailoc up`, `reload` and `migrate` warn about it, and you add the zone of the canonical name to `internal_zones`.
- **Running VMs after an upgrade**: the network broker of a workspace VM started by a jailoc release without internal zones keeps the old behaviour, so `jailoc reload` and `jailoc up` refuse to update its network policy and ask for `jailoc restart`.

```toml
[defaults.network]
internal_zones = ["internal.example.com"]

[[workspaces.api.network.rules]]
name = "gitlab"
allow = ["gitlab.internal.example.com"]   # 10.20.5.10: opened

[[workspaces.api.network.rules]]
name = "saas"
allow = ["*.vercel.app"]                   # a private answer is dropped
```

### Example

```toml
[defaults.network]
ipv6 = true
dns = "9.9.9.9"

[[defaults.network.rules]]
name = "dev-deps"
allow = ["github.com", "api.github.com", "*.githubusercontent.com", "registry.npmjs.org"]

[[defaults.network.rules]]
name = "no-paste"
deny = ["pastebin.com", "gist.github.com", "*.ngrok.io"]

[[workspaces.api.network.rules]]
name = "db"
allow = ["10.20.5.10"]
proto = "tcp"
ports = [5432]
```

---

## `[workspaces.<name>]`

Each workspace is a TOML table under `[workspaces]`, keyed by name. Names must match `^[a-z0-9-]+$`.

### Workspace-only fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `paths` | string[] | `[]` | Host directories shared into the VM over virtio-fs at their original absolute paths. See [`paths`](#paths). |
| `dockerfile` | string | (none) | Local path (`/...`, `~/...`) or HTTP(S) URL to an overlay Dockerfile built on top of the base image. |
| `build_context` | string | (none) | Build context of the overlay build. |

- `paths`: the first path is the working directory; with no paths, the working directory is `/home/agent`.
- `dockerfile`: build failure is fatal. Maximum file size for HTTP sources: 1 MiB.
- `build_context`: when empty, the Dockerfile's parent directory (local path) or a temporary directory (URL).

### Overriding fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `backend` | string | (inherit) | `"vm"` or empty. All other values are rejected. |
| `agent` | string | (inherit) | Coding agent of the workspace. See [Coding agents](#coding-agents). |
| `agents` | table | `{}` | Settings per coding agent; a set field overrides `[defaults.agents.<name>]`. See [`agents`](#agents). |
| `image` | string | (none) | Pre-built image used directly, bypassing all build steps. Cannot be combined with `dockerfile` or `build_context`. |
| `network` | table | `{}` | Same fields as [`[defaults.network]`](#defaultsnetwork); a set field wins. |

Fields with the same meaning as in [`[defaults]`](#defaults); the workspace value wins and the default is inherited. Each takes the type of its `[defaults]` field:

- `ssh_auth_sock`, `gpg_agent`, `git_config`, `import_agent_config`
- `cpu`, `memory`, `docker_disk`, `tmp_storage`, `tmp_size`, `tmp_ttl`, `timezone`, `notifications`
- `enable_docker`, `protect_git`, `isolated_build`, `ca_bundle`

### Merged fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `mounts` | string[] | `[]` | Directory shares for this workspace, merged with `[defaults].mounts`. |
| `snapshots` | string[] | `[]` | Read-only snapshots, merged with `[defaults].snapshots`. |
| `allowed_hosts` | string[] | `[]` | Merged with `[defaults].allowed_hosts`. |
| `allowed_networks` | string[] | `[]` | Merged with `[defaults].allowed_networks`. |
| `ports` | (int \| string)[] | `[]` | Merged with `[defaults].ports`. |
| `host_ports` | (int \| string)[] | `[]` | Merged with `[defaults].host_ports`. |
| `env` | string[] | `[]` | Overrides `[defaults].env` entries with the same key. |
| `env_file` | string[] | `[]` | Loaded after `[defaults].env_file`. |
| `secrets` | table | `{}` | Overrides default secrets with the same name. |
See [Merge Semantics](#merge-semantics) for the exact rules.

### Example

```toml
[workspaces.my-project]
paths = ["~/projects/my-project", "~/shared/libs"]
allowed_hosts = ["api.example.com"]
snapshots = ["~/.npmrc:/home/agent/.npmrc"]
ports = [3000, "8080:80"]
dockerfile = "~/projects/my-project/overlay.Dockerfile"
```

---

## Coding agents

Each workspace runs one coding agent. By default, sessions start it in unattended mode, because the VM is the security boundary. The flags that enable unattended mode are the agent's [`default_args`](#agents), which a configuration can replace or clear.

`opencode` is an alias of `opencode2`: it runs the same harness and shares its persistent home directories, while `agents.opencode` and `agents.opencode2` are separate tables.

**Harness and package**

| `agent` | Agent | Package |
|---------|-------|---------|
| `opencode2` | OpenCode 2 | `@opencode/cli` |
| `opencode1` | OpenCode 1 | `opencode-ai` |
| `claude` | Claude Code | `@anthropic-ai/claude-code` |
| `copilot` | GitHub Copilot CLI | `@github/copilot` |
| `omo` | OmO | `omo-ai` |
| `agy` | Antigravity CLI | GitHub release `google-antigravity/antigravity-cli` |

**Default session command and flags**

| `agent` | Session command | Continue (`-c`) | Resume (`-s ID`) |
|---------|-----------------|-----------------|------------------|
| `opencode2` | `opencode --standalone --auto` | `--continue` | `--session ID` |
| `opencode1` | `opencode` | `--continue` | `--session ID` |
| `claude` | `claude --dangerously-skip-permissions --settings /etc/jailoc-tui-plugin/claude-settings.json` | `--continue` | `--resume ID` |
| `copilot` | `copilot --allow-all` | `--continue` | `--resume ID` |
| `omo` | `omo --permission-preset full-access --extension /etc/jailoc-tui-plugin/omo.js` | `--continue` | `--session ID` |
| `agy` | `agy --dangerously-skip-permissions` | `--continue` | `--conversation ID` |

**Persistent home directories**

| `agent` | Directories |
|---------|-------------|
| `opencode2` | `~/.local/share/opencode`, `~/.local/state/opencode` |
| `opencode1` | `~/.local/share/opencode`, `~/.local/state/opencode`, `~/.omo` |
| `claude` | `~/.claude` |
| `copilot` | `~/.copilot` |
| `omo` | `~/.omo` |
| `agy` | `~/.gemini` |

**Environment set by jailoc**

| `agent` | Variables |
|---------|-----------|
| `opencode2` | `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_CLI_CONFIG_CONTENT` |
| `opencode1` | `OPENCODE_DISABLE_AUTOUPDATE=1`, `OPENCODE_TUI_CONFIG` |
| `claude` | `CLAUDE_CONFIG_DIR=/home/agent/.claude`, `CLAUDE_CODE_SANDBOXED=1`, `DISABLE_AUTOUPDATER=1`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` |
| `copilot` | `COPILOT_AUTO_UPDATE=false` |
| `omo` | `PI_SKIP_VERSION_CHECK=1`, `OMO_RUNTIME=node` |
| `agy` | `AGY_CLI_DISABLE_AUTO_UPDATE=1` |

**Optional default arguments**

| `agent` | Built-in `default_args` |
|---------|-------------------------|
| `opencode2`, `opencode` | `["--auto"]` |
| `opencode1` | `[]` |
| `claude` | `["--dangerously-skip-permissions"]` |
| `copilot` | `["--allow-all"]` |
| `omo` | `["--permission-preset", "full-access"]` |
| `agy` | `["--dangerously-skip-permissions"]` |

`--standalone` of `opencode2`, and the plugin flags `--settings` and `--extension`, are not default arguments: they are always present.

**Session command line**: the agent binary, its always-present `--standalone` (for `opencode2`), the effective `default_args`, the plugin flags (`--settings`, `--extension`), the continue or resume flag, then `args` from [`agents`](#agents). The default session command table shows the line with the built-in `default_args`.

**Agent-specific behavior**

- OpenCode 2 runs with `--standalone`, so each session has its own server and environment instead of sharing a background service.
- Every agent shows the workspace name. `OPENCODE_TUI_CONFIG`, `OPENCODE_CLI_CONFIG_CONTENT`, `--settings` and `--extension` load the jailoc indicator. See [Workspace indicator](../how-to/tui-plugin.md).
- For `copilot` and `agy`, jailoc adds a `statusLine` to `~/.copilot/settings.json` and `~/.gemini/antigravity-cli/settings.json` when it sets none. For `copilot` it also sets `storeTokenPlaintext = true` in `~/.copilot/settings.json` when unset, so Copilot stores logins without a system keyring.
- For `claude`, jailoc sets `bypassPermissionsModeAccepted = true` in `~/.claude/.claude.json` on every VM start when the file does not set it, and `CLAUDE_CODE_SANDBOXED=1` makes Claude Code trust the working directory without its trust prompt. Background sessions (`claude --bg`, which switching sessions in Claude Code uses) start only after both, and would otherwise ask to run `claude --dangerously-skip-permissions` interactively once.

**Installation**

- The package versions are pinned by the jailoc release; `version` in [`agents`](#agents) installs another one.
- jailoc installs the agent with `npm install -g`. For `agy` it downloads the release asset of the VM's architecture (x64 or arm64, glibc), checks its pinned SHA-256 and installs the binary as root into `/usr/local/bin`.
- The agent goes into a separate layer on top of the base image, before the workspace `dockerfile` overlay, in every image jailoc builds: the [base image](image-resolution.md#step-5-embedded-dockerfile), a `[base] dockerfile`, and `defaults.image` with a workspace `dockerfile`.
- An image used directly (workspace `image`, or `defaults.image` without a workspace `dockerfile`) must contain the agent binary.
- Before a session starts, jailoc checks that the binary is on `PATH` in the VM.

Changing `agent` or the agent's `profile` of a running workspace takes effect after `jailoc restart`; sessions fail until then.

### `agents`

Settings per agent live in `[defaults.agents.<name>]` and `[workspaces.<ws>.agents.<name>]`, where `<name>` is a built-in agent or a [custom agent](#custom-agents). Only the settings of the selected agent apply.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `from` | string | | Custom agents only: the built-in agent it is derived from. |
| `install` | bool | `true` | Install the agent in the images jailoc builds. With `false`, the image must provide the binary, for example through the workspace `dockerfile`. |
| `version` | string | (pinned) | npm or release version installed instead of the version pinned by jailoc, e.g. `"2.1.400"`. |
| `default_args` | string[] | (built-in) | Optional default arguments of the session command line, such as the flags that enable unattended mode. A set list replaces the built-in ones. See the built-in values under [Coding agents](#coding-agents). |
| `args` | string[] | `[]` | Arguments appended to every session command line, after `default_args`. |
| `mounts` | string[] | `[]` | Mounts added while the agent is selected. See [Agent mounts](#agent-mounts). |
| `profile` | `false` or string | (unset) | Where the agent's persistent home directories live. See [Agent profiles](#agent-profiles). |

Field notes:

- `version`: must be an exact version; dist-tags such as `latest` are rejected. For `agy`, jailoc reads the checksums of another version's assets from the GitHub API.
- `default_args`: an unset field inherits the value from `[defaults]` (or the base agent for a custom agent), falling back to the built-in `default_args` of the agent's harness. `default_args = []` clears them, and a non-empty list replaces them. Each entry is one argument. It doesn't touch the always-present arguments (`--standalone` of `opencode2`, `--settings` and `--extension` of the plugin) or `args`. Each of `default_args` and `args` is inherited independently of the other.
- `default_args` is not a permission policy. Removing a flag doesn't prevent the agent from running without prompts: imported agent settings, or entries in `args`, can still enable a permission bypass. The VM remains the boundary.
- `args`: a workspace list replaces the `[defaults]` list; `args = []` clears it. `args = []` removes only the extra arguments you added; the built-in `default_args` stay. `default_args = []` removes the built-in defaults; `args` stay.
- A change to `default_args` or `args` applies to sessions started afterwards (new, resumed or continued). A running attached session keeps its arguments. Neither needs `jailoc restart` or `jailoc reload`.
- `mounts`: same syntax and validation as [`mounts`](#mounts). An empty host (`":/home/agent/.agents"`) removes a mount added by the agent or an earlier table.
- `profile` values: unset means per workspace; `false` means per workspace, emptied on every VM start; a name selects the shared profile `~/.local/share/jailoc/profiles/<name>`.

```toml
[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "copilot"

[workspaces.my-project.agents.copilot]
version = "1.0.110"
args = ["--model", "gpt-5"]
```

### Custom agents

An `agents` table whose name is not a built-in agent defines a custom agent: the built-in agent named by `from` under another name, with its own settings. Select it with `agent` like a built-in one. It is installed, started and persisted like the agent it is derived from.

```toml
[defaults.agents.claude]
version = "2.1.400"

[defaults.agents.claude-opus]
from = "claude"
args = ["--model", "opus"]

[workspaces.my-project]
paths = ["~/projects/my-project"]
agent = "claude-opus"
```

- A custom agent defined in `[defaults]` can be selected and configured in any workspace; one defined in a workspace only there.
- Each field except `mounts` and `profile` takes the first value set in, in this order (`default_args` counts as set when written, including `[]`):
    1. the workspace's table of the agent
    2. the defaults' table of the agent
    3. for a custom agent, the workspace's table of the agent it is derived from
    4. for a custom agent, the defaults' table of the agent it is derived from
- In the example, `claude-opus` installs Claude Code 2.1.400.

### Agent mounts

The mounts of the selected agent are merged by guest path; a later entry replaces an earlier one with the same guest path, an empty host removes it. The order is:

1. the built-in mounts of the agent's harness, including `~/.agents:/home/agent/.agents:ro`
2. `mounts` of the built-in agent's table in `[defaults]`, then in the workspace
3. for a custom agent, `mounts` of its own table in `[defaults]`, then in the workspace
4. `[defaults].mounts`, then the workspace's `mounts`

```toml
[defaults.agents.opencode-legacy]
from = "opencode"
mounts = ["~/.config/opencode-legacy:/home/agent/.config/opencode"]
```

A mount at the destination of an [`import_agent_config`](#import_agent_config) copy replaces the copy.

### Agent profiles

`profile` selects where the agent's persistent home directories (see [Coding agents](#coding-agents)) are stored. It is read from the selected agent's own tables: the workspace's, then the defaults'. A custom agent does not inherit `profile` from the agent it is derived from.

| Value | Storage | Lifetime |
|-------|---------|----------|
| unset | `~/.cache/jailoc-vm/workspaces/<workspace>/guest/home/<dir>` | until `jailoc down --purge` |
| `false` | per workspace | emptied on every VM start |
| `"<name>"` | `~/.local/share/jailoc/profiles/<name>/<dir>` | until deleted by hand; `jailoc down --purge` keeps it |

`<dir>` is the name of the harness's state directory:

| Harness | `<dir>` |
|---------|---------|
| OpenCode 2 | `opencode2-data`, `opencode2-state` |
| OpenCode 1 | `opencode-data`, `opencode-state` |
| OmO plugin's `~/.omo` (with OpenCode 1) | `opencode1-omo` |
| Claude Code | `claude` |
| Copilot | `copilot` |
| OmO | `omo` |
| Antigravity | `antigravity` |

- One profile can hold the state of several harnesses, and two agents of the same harness share it when they name the same profile.
- `~/.cache` stays per workspace.
- A mount over a persistent home directory replaces it.
- A named profile is used by one running workspace at a time: `jailoc up` and `jailoc restart` fail while another workspace using the profile is running.

!!! warning
    Every workspace using a named profile can change what the harness executes in the others (hooks, plugins, MCP servers, instructions stored in its state directory), read the login and resume the sessions stored there. Share a profile only between workspaces of the same trust level. See [Threat Model](../explanation/threat-model.md#limiting-private-data-access).

```toml
[workspaces.old-project.agents.opencode]
profile = "opencode-legacy"
```

---

## Validation Rules

Rules checked at config load or at `jailoc up` and `jailoc reload`, per setting.

<a id="jailoc-v1-settings"></a>

### Unsupported configuration

Any `backend` value other than `"vm"` and empty is rejected. For a config-load error naming a rejected setting, see [Upgrade an existing installation](../how-to/migrate-from-v1.md#recognize-a-v1-configuration).

### Moved network settings

`filtered_dns`, `dns_upstream` and `dns_blocked_zones` directly in `[defaults]` or a workspace fail config load with ``[defaults]: filtered_dns moved to the [defaults.network] table; run `jailoc migrate` to update the config`` (or the workspace's `network` table). `jailoc migrate` moves them.

### `paths`

- `~` is expanded.
- An empty list is allowed; empty entries are rejected.
- Each path must exist on the host and be a directory. `jailoc up` and `jailoc reload` fail with an error naming the workspace and path otherwise; `jailoc` does not create a missing path. This differs from read-write [`mounts`](#mounts), whose missing host directories are created.
- Paths equal to or under these prefixes are rejected: `/home/agent`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/opt`, `/root`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/certs`.
- Several workspaces may share a path, for example to run different agents in the same project. When the current directory matches several workspaces at the same depth, `jailoc` fails and asks for `-w`.

**Share limit.** Each path is a separate virtio-fs share. A VM boots with at most:

| Host | Shares |
|------|--------|
| linux/amd64 | 11 |
| arm64 (macOS and Linux) | 120 |

- With [`enable_docker`](#enable_docker) the limit is one fewer, and with `tmp_storage = "disk"` one fewer.
- Paths, [`mounts`](#mounts) with a host directory and the state directories of a named [agent profile](#agent-profiles) count towards the limit.
- `jailoc up` and `jailoc reload` fail above the limit. List a common parent directory instead of many sibling directories.

### `mounts`

Format: `host:container[:mode]`, mode `ro` or `rw` (default `rw`). Mounts are virtio-fs directory shares.

**`host`**

- Absolute or `~` path. Must be a directory; a file mount fails at `jailoc up`. Use [`snapshots`](#snapshots) for files.
- A missing host directory of a read-write mount is created.
- A missing host directory of a read-only mount fails `jailoc up` and `jailoc reload`, except for the built-in mounts of the agent's harness (see [Merge Semantics](#merge-semantics)), which are created.
- An empty host (`":/home/agent/.agents"`) removes an inherited mount with that destination.
- `jailoc up` and `jailoc reload` warn about a symbolic link directly inside a path or mount that resolves to a file under the home directory that no path, mount or snapshot makes available at the path the link names; the link is dangling in the VM. `jailoc reload` checks the paths and mounts the VM runs with, which change only on restart.

**`container`**

- Absolute, or `~/...`, where `~` stands for the guest home `/home/agent`.
- Rejected when equal to or under `/`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`, `/opt`, `/root`, `/proc`, `/sys`, `/dev`, `/run`, `/tmp`, `/certs`.
- Destinations under `/home/agent/...` are allowed.

**Rejected host paths.** Host paths equal to or under these entries are rejected:

- System directories: `/`, `/boot`, `/dev`, `/etc`, `/private`, `/proc`, `/sys`, `/run`, `/var`
- Credential directories and files: `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.azure`, `~/.config/gcloud`, `~/.config/gh`, `~/.docker`, `~/.kube`, `~/.netrc`
- jailoc configuration and state: `~/.config/jailoc`, `~/.cache/jailoc-vm`

Matching details:

- Parents of the entries under `~` are rejected too, so `~`, `~/.config` and `~/.cache` cannot be shared whole.
- Entries under `~` are also compared after resolving symlinks, so a symlink to `~/.ssh`, or a dotfiles directory that `~/.ssh` links into, is rejected as well.

**Error message.** The error explains why the matched entry is protected:

- for credential directories it shows SSH or GPG agent forwarding or a [file secret](#secrets) holding the one file a tool needs;
- for `~/.config/jailoc` and `~/.cache/jailoc-vm` it suggests keeping shared directories outside them or using an [agent profile](#agent-profiles).

See [Pass a credential file](../how-to/secrets.md#pass-a-credential-file).

### `snapshots`

Format: `host:container`, optionally `:ro`; `:rw` is rejected because snapshots are read-only. Host and destination rules are the same as for `mounts`, except that the host may be a file or a directory. An empty host removes an inherited snapshot.

- jailoc copies the sources when the VM starts and again on every `jailoc reload`, which also applies added and removed entries.
- At `jailoc up` and `jailoc reload`, the destination must not equal, contain or lie inside a shared path (workspace `paths`, `mounts` destinations, the persistent home directories), and the source must exist.
- A symlink given as the source root is resolved; symlinks inside a copied directory are copied unchanged.

### `git_config`

When `true`, jailoc adds read-only snapshots of the host Git configuration at `jailoc up` and `jailoc reload`:

| Host | Guest |
|------|-------|
| `~/.gitconfig` | `/home/agent/.gitconfig` |
| `$XDG_CONFIG_HOME/git/config` | `/home/agent/.config/git/config` |
| `$XDG_CONFIG_HOME/git/ignore` | `/home/agent/.config/git/ignore` |
| `$XDG_CONFIG_HOME/git/attributes` | `/home/agent/.config/git/attributes` |

- `$XDG_CONFIG_HOME` defaults to `~/.config`.
- Missing files are skipped. Other files in the directory are not copied.
- The Git credential stores `~/.git-credentials`, `$XDG_CONFIG_HOME/git/credentials` and `~/.config/git/credentials` are never copied, whichever path or link names them.
- At most 256 configuration files are read.

**Referenced files.** Files referenced by the copied configuration files are copied to the same path relative to `/home/agent` when they lie under the host home directory. The referencing settings are:

- `include.path` and `includeIf.<condition>.path`: followed recursively, relative paths resolved against the including file; the included file must stay under the home directory or `$XDG_CONFIG_HOME`
- `core.excludesFile`
- `core.attributesFile`
- `commit.template`
- `gpg.ssh.allowedSignersFile`
- `user.signingKey` when its value is a path

**Path and credential checks.**

- The home directory check uses the path as written, so a symlink under the home directory may point elsewhere, such as a dotfiles checkout or the Nix store.
- The credential checks apply to both the path as written and the file it resolves to, including hard links to files in `~/.ssh`, `~/.gnupg` and `~/.aws` and in the directories symlinks there point to. The copy holds the content that was checked.

**Not copied, with a warning.** A referenced file is not copied and produces a warning when it is:

- missing, or outside the home directory;
- not a regular file of at most 1 MiB;
- a Git credential store;
- in `~/.ssh`, `~/.gnupg` or `~/.aws`;
- or when its path in the VM is already taken by a different file.

**Exceptions: public data only.** `gpg.ssh.allowedSignersFile` and `user.signingKey` are exceptions to the directory rule but copy public data only:

- The `user.signingKey` copy holds the first SSH public key in the file.
- The `gpg.ssh.allowedSignersFile` copy holds its signer lines (principals, the `cert-authority`, `namespaces`, `valid-after` and `valid-before` options, and the public key) without comments.
- A `user.signingKey` file without an SSH public key, or an allowed signers file with any other line, is not copied and produces a warning.

**Rewrites.** Configuration files are copied with two rewrites:

- values of the path variables above that lie under the host home directory become `~/...`;
- `includeIf "gitdir:~/..."` and `includeIf "gitdir/i:~/..."` conditions use the host home directory, which workspace paths keep inside the VM.

**Overriding.** Each copy is a default snapshot: a [`snapshots`](#snapshots) entry with the same destination replaces it, an entry with an empty host removes it, and a directory snapshot that contains its destination replaces it.

### `import_agent_config`

When `true`, jailoc adds read-only snapshots of the selected agent's host configuration at `jailoc up` and `jailoc reload`. Each host path is copied to the same path under `/home/agent`.

| Agent | Host paths |
|-------|------------|
| `opencode`, `opencode2` | `~/.config/opencode`, without `node_modules` |
| `opencode1` | `~/.config/opencode`, without `node_modules`; `~/.omo/omo.jsonc`, `~/.omo/omo.json` |
| `claude` | `~/.claude/CLAUDE.md`, `~/.claude/RTK.md`, `~/.claude/settings.json`, `~/.claude/agents`, `~/.claude/commands`, `~/.claude/hooks`, `~/.claude/skills`, `~/.claude/output-styles` |
| `copilot` | `~/.copilot/copilot-instructions.md`, `~/.copilot/agents`, `~/.copilot/hooks`, `~/.copilot/skills`, `~/.copilot/mcp-config.json` |
| `agy` | `~/.gemini/GEMINI.md` |
| `omo` | `~/.omo/omo.jsonc`, `~/.omo/omo.json` |

- Missing sources are skipped.
- Logins, sessions and the agents' own `config.json` and `settings.json` of `copilot` stay in the persistent state directory.
- The OpenCode copy gets the `.gitignore` OpenCode would write (unless the host directory has one).
- OpenCode doesn't install the dependencies of local plugins in the read-only directory: plugins listed in `plugin` still install into `~/.cache`, but a local plugin that imports a package needs it installed in the image.
- The copies inside a persistent state directory (`~/.claude`, `~/.copilot`, `~/.omo`, `~/.gemini`) hide what is there in the VM: a skill the agent installs into `~/.claude/skills` is not visible while that directory is copied. When the snapshot is dropped, the empty mount points it left in the state directory are removed at the next start.

**Overriding.** Each copy is a default snapshot like the [Git configuration](#git_config): a [`snapshots`](#snapshots) entry with the same destination replaces it, an entry with an empty host removes it (`snapshots = [":/home/agent/.claude/settings.json"]`), and a directory snapshot that contains its destination replaces it. A copy whose destination equals, contains or lies inside a shared path, such as a mount of `~/.claude` or a [named profile](#agent-profiles) state directory, is skipped.

### `ports`

Each entry is a port number or `"HOST:GUEST"`, with ports 1 to 65535.

| Entry | Meaning |
|-------|---------|
| `3000` | guest 3000 on host 3000 |
| `"8080:80"` | guest 80 on host 8080 |

- A host port may appear only once per list.
- A workspace entry replaces a `[defaults]` entry with the same host port.
- Ports are published on host `127.0.0.1` only.

### `host_ports`

Each entry makes a host service reachable from the guest:

| Entry | Guest port | Host target |
|-------|------------|-------------|
| `9222` | 9222 | `127.0.0.1:9222` |
| `"8931:3845"` | 8931 | `127.0.0.1:3845` |
| `"7100:/tmp/mcp.sock"` | 7100 | Unix socket `/tmp/mcp.sock` (absolute or `~/`) |

- Ports are 1 to 65535.
- A guest port may appear only once per list; a workspace entry replaces a `[defaults]` entry with the same guest port.
- Guest ports 22, 53, 2375, 7022 and 7023 are reserved.
- Inside the guest each port is reachable on the gateway `192.168.127.1`, on the names `host.jailoc.internal` and `host.docker.internal`, and on `127.0.0.1` and `[::1]` when nothing in the guest already listens on that port.
- TCP targets are always host loopback.
- A Unix socket that does not exist yet produces a warning; connections fail until it exists.
- jailoc warns when an entry targets port 9222 (Chrome DevTools Protocol) or a container runtime socket (`docker.sock`, `containerd.sock`, `podman.sock`, `buildkitd.sock`).

!!! warning
    Host services reached through `host_ports` bypass the network policy. Anything in the guest gets the same access to them as a local process on the host.

### `allowed_hosts` and `allowed_networks`

- `allowed_networks` entries must parse with Go's `net.ParseCIDR`; IPv4 and IPv6 CIDRs are accepted.
- `allowed_hosts` IP addresses and CIDRs are allowed as they are.
- `allowed_hosts` hostnames become hostname allow rules and [internal zones](#internal-zones); they are not resolved on the host.
- Some IPv6 ranges stay denied regardless of these lists; see [Network isolation](../explanation/network-isolation.md).

### Filtered DNS

When `network.filtered_dns = true`, an effective `dns_upstream` and at least one effective `dns_blocked_zones` entry are required.

**`dns_upstream`**: a numeric IPv4 address other than unspecified, loopback, link-local or multicast.

**Each blocked zone**: a DNS name of labels with letters, digits or internal hyphens (63 characters per label, 253 total); a trailing dot is accepted and `"."` is rejected.

**Filtering behavior**:

- Queries for names in blocked zones get NXDOMAIN, matched case-insensitively including subdomains, and so do answers that name a blocked zone anywhere, for example through a CNAME.
- Queries with more or fewer than one question are refused.
- Direct DNS to port 53 of other servers is blocked unless an `allow` rule opens it (see [Direct DNS block](#implicit-rules-and-built-in-broker-behavior)), and such queries get the same filtering.

With filtering on, an effective `allowed_hosts` entry equal to or under an effective blocked zone is rejected at config load.

### `ca_bundle`

| Value | Effect |
|-------|--------|
| omitted or `true` | Automatic discovery. The non-empty `SSL_CERT_FILE` environment variable is used first, then `NIX_SSL_CERT_FILE`. If the selected variable points at an invalid source, `jailoc up` fails without trying the next one. If neither is set, no bundle is forwarded. |
| `false` | No bundle. |
| non-empty string | Custom bundle path, absolute or `~`. Symlinks are resolved; the target must be a readable regular file with at least one `CERTIFICATE` PEM block and no private-key PEM block. |
| empty string or any other TOML type | Rejected at config load. |

**Trust store.**

- The bundle is added to the VM's system trust store, which its Docker daemon also uses, and to the builder VM's Docker daemon.
- In the VM, `/etc/ssl/certs/ca-certificates.crt` then holds the image's roots plus the forwarded certificates, also when the image keeps its own bundle elsewhere.
- Each forwarded certificate is also written to `jailoc/` under `/usr/local/share/ca-certificates` or `/etc/pki/ca-trust/source/anchors`, if the image has that directory, so running `update-ca-certificates` or `update-ca-trust` in a session keeps it.
- When a reload removes or replaces these certificates, jailoc runs that tool, if the image has it, so stores it generated stop trusting the old ones.

**Environment variables.** When a bundle is forwarded, sessions get `NODE_USE_SYSTEM_CA=1` and these variables set to `/etc/ssl/certs/ca-certificates.crt`, for clients that ship their own roots or read only a variable:

| Variable | Read by |
|----------|---------|
| `NODE_EXTRA_CA_CERTS` | Node.js, Bun and the agents built on them |
| `SSL_CERT_FILE` | OpenSSL, Go, uv, httpx |
| `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE` | Python requests, curl |
| `PIP_CERT` | pip |
| `AWS_CA_BUNDLE` | AWS CLI and SDKs |
| `CLOUDSDK_CORE_CUSTOM_CA_CERTS_FILE` | Google Cloud CLI |
| `HTTPLIB2_CA_CERTS` | httplib2 |
| `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` | gRPC |
| `CARGO_HTTP_CAINFO` | cargo |
| `GIT_SSL_CAINFO` | git |
| `NIX_SSL_CERT_FILE` | Nix |
| `DENO_CERT` | Deno |
| `HEX_CACERTS_PATH` | Hex |

A variable set by the workspace's `env`, `env_file` or an env secret takes precedence.

**Inspection CA.**

- When any network rule has an [`http` table](#http-rules), the workspace's inspection CA certificate is appended to the bundle, also with `ca_bundle = false` (the bundle then holds only that certificate), so the guest trusts it and the variables above are set.
- A `ca_bundle` path is also the extra trust anchor for the broker's upstream certificate checks.

### `enable_docker`

- With `enable_docker = true`, jailoc starts `dockerd` inside the workspace VM; an image without `dockerd` fails `jailoc up`.
- `enable_docker = true` together with `protect_git = true` is rejected, because Docker access is root access inside the VM. `protect_git` alone turns Docker off.
- In the images jailoc builds, Docker is installed only when `enable_docker` is on; see [Docker layer](image-resolution.md#docker-layer-steps-2-4-and-5).

### `protect_git`

Workspace paths are shared read-write, including `.git`. Git on the host runs hooks and commands named in `.git/config` (such as `core.fsmonitor`), so an agent that writes there can run code on the host. `protect_git = true` makes the hooks and configuration listed below read-only inside the VM.

```toml
[workspaces.myproject]
protect_git = true
```

!!! warning
    `protect_git = true` disables `sudo` and Docker in that workspace. Combining it with `enable_docker = true` is rejected.

!!! warning
    Only repositories that exist at `jailoc up` are covered. Repositories the agent creates or clones later aren't protected until the next restart, and repositories inside `node_modules`, `.venv`, `venv`, `__pycache__`, `.tox`, `.terraform`, `.next`, `.gradle` and `.cache` aren't searched. Files that tools run from the working tree, such as `Makefile` or `package.json` scripts, aren't covered either. The protection is enforced inside the guest; it holds against the agent, not against an exploit of the guest kernel.

**Repositories covered.** At every `jailoc up`, jailoc finds the repositories, including bare ones, under the workspace paths and read-write `mounts`, including symlinked workspace roots.

- A repository visible at several paths in the VM is protected at each of them.
- A repository hidden under a read-only mount is skipped.
- The search fails when it scans more than 500,000 directories under the writable shares (the error ends with `protect_git needs narrower paths`). Skipped directory names do not count.

**Read-only inside the VM.**

- `.git/hooks`, `.git/config` and `config.worktree`
- linked worktree `commondir` pointers
- every `core.hooksPath` directory:
    - a relative `core.hooksPath` is protected both below the working tree and below the git directory;
    - values from the host's system and global Git configuration count too, so a host-wide hooks directory inside a share is protected.
- files named by `include.path` or `includeIf`, whether or not the `includeIf` condition matches
- the same metadata of submodules, worktrees and separate git directories reached through `.git` files
- the files that symlinked hooks point at

**Side effects.**

- A missing `.git/config` or `config.worktree` is created empty so that it can be protected.
- These git directories and their parents cannot be renamed or removed.

**`jailoc up` fails when:**

- the path to a protected file runs through a symbolic link inside a share, because the link itself could be redirected;
- a protected file or hook has another hard link;
- a configuration file is not a regular file or is larger than 1 MiB;
- a configuration includes more than 256 files;
- a hook links to a missing file;
- a hooks directory has more than 1024 entries;
- a `commondir` points at another linked git directory.

### `cpu`, `memory` and `docker_disk`

| Field | Rule |
|-------|------|
| `cpu` | A finite number greater than 0. |
| `memory` | Must match `^[1-9][0-9]*[kmgKMG]?$` (for example `512m`, `4g`). At `jailoc up`, at least `512m`. |
| `docker_disk` | Must match `^[1-9][0-9]*[kmgKMG]?$` (for example `512m`, `4g`). At `jailoc up`, at least `1g`, and rounded up to whole MiB. |

When the existing data disk is larger than `docker_disk`, `jailoc up` fails instead of shrinking it.

`[vm] builder_disk` follows the same format and grow-only rule, with at least `1g`, checked when the shared builder VM starts for a build, not by validation.

### `tmp_storage`, `tmp_size` and `tmp_ttl`

These settings apply to the guest `/tmp` only. `/var/tmp`, `~/.local/tmp`, `/run/secrets`, persistent caches and directories named by an overridden `TMPDIR` are not affected. A workspace value wins over `[defaults]`, which wins over the built-in value.

| Field | Valid values | Default | Applied by |
|---|---|---|---|
| `tmp_storage` | `"ram"`, `"disk"` | `"ram"` | `jailoc restart` |
| `tmp_size` | positive integer of bytes, optionally followed by `k`, `m` or `g` (binary multiples, either case); at least `16m` with `"disk"` | `"1g"` | `jailoc restart` |
| `tmp_ttl` | positive Go duration (`time.ParseDuration`, e.g. `"15m"`, `"8h"`) or `"0"` | `"8h"` | `jailoc reload` |

`tmp_ttl = "0"` disables the cleanup. A value outside these rules is rejected. A VM started by a jailoc release with an older guest protocol (before protocol 4) needs `jailoc restart` before it uses any of these settings, including `tmp_ttl`.

#### Storage

| `tmp_storage` | `/tmp` lives in | Capacity |
|---|---|---|
| `ram` | a tmpfs in the VM's memory | `tmp_size` is an upper bound. The pages count against the VM's `memory`, and idle ones go to compressed swap (zram). The host reserves nothing, and its free disk space is irrelevant. A cap above `memory` does not add room. |
| `disk` | an ext4 file system on a private sparse disk | `tmp_size` is the size of the disk. ext4 metadata reduces the available capacity below it. The sparse file is a cap, not a reservation: the host stores only the blocks the guest has written. |

With `disk`:

- The disk is private to the workspace. It is the file `tmp.img` in the workspace state directory on the host, outside every guest share, and it is independent of the Docker data disk ([`docker_disk`](#cpu-memory-and-docker_disk)).
- jailoc creates the disk empty at every VM start and formats it with `mkfs.ext4` from the workspace image. Nothing in `/tmp` survives a VM stop or restart.
- The image must contain `mkfs.ext4` (package `e2fsprogs`). The embedded base image does. An image without it fails the VM start with an error that names `mkfs.ext4`; jailoc installs nothing. See [Use a custom image](../how-to/custom-images.md#know-what-the-image-must-provide).
- The guest trims freed blocks periodically (`FITRIM`) so that the host can reclaim them, and jailoc removes the disk file when the VM stops.

#### Mount options

`/tmp` has mode `1777` and is mounted `nosuid`, `nodev`, `strictatime` and executable, so access times are exact and build tools and test runners can run files from it.

#### Cleanup

With `tmp_ttl` above zero, the guest sweeps `/tmp` every 15 minutes. The cutoff is the current time minus `tmp_ttl`.

| Entry | Removed when |
|---|---|
| Regular file | its access, modification and change times are all older than the cutoff |
| Directory | it is empty and its access and modification times are older than the cutoff; its change time is ignored |

The sweep skips:

- entries that another process holds an `flock` on;
- sockets, symbolic links, devices and FIFOs;
- other mounts below `/tmp`; the sweep does not cross into them.

The checks are best effort. A file renamed into a checked name during a sweep can be removed in its place, and a program that keeps a file open without an `flock` does not protect its path, though it keeps reading the unlinked file through its descriptor. Removal happens at the first sweep after the cutoff, not at exactly creation time plus `tmp_ttl`.

The default `8h` is more aggressive than the 10 days that distributions typically keep `/tmp` files. The cleanup cannot stop a running job from filling `/tmp` before the TTL passes; the capacity limit still applies.

#### Session environment

Sessions get `TMPDIR`, `TMP`, `TEMP` and `GOTMPDIR` set to `/tmp`. A variable set by the image configuration, the workspace's `env` or `env_file`, an env secret, or the command itself takes precedence.

```toml
[defaults]
tmp_storage = "ram"
tmp_size = "2g"

[workspaces.build]
tmp_storage = "disk"
tmp_size = "20g"
tmp_ttl = "0"
```

### `timezone`

- `timezone` must match `^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`; absolute paths and POSIX `TZ` rules are rejected.
- Without it, the VM takes the host's zone from `TZ` when set, otherwise from `/etc/localtime`.
- `jailoc-guest` points `/etc/localtime` at the zone in the image's `/usr/share/zoneinfo`, or copies the host's data for that zone when the image lacks it, and writes the name to `/etc/timezone`.
- A zone that neither the image nor the host has leaves the image's zone in place.
- [`jailoc reload`](cli.md#jailoc-reload) applies a changed `timezone` to a running VM.

### `notifications`

`notifications` must be `"auto"`, `"native"`, `"osc"` or `"off"`. jailoc reads it on the host each time a session attaches, so a change applies at the next attach, without `jailoc reload` or `jailoc restart`.

| Value | Transport |
|-------|-----------|
| `"auto"` | `"native"` when the host can show desktop notifications and neither `SSH_CONNECTION` nor `SSH_TTY` is set, otherwise `"osc"` |
| `"native"` | A desktop notification from a host command: `/usr/bin/osascript` on macOS, `notify-send` on `PATH` on Linux with `WAYLAND_DISPLAY`, `DISPLAY` or `DBUS_SESSION_BUS_ADDRESS` set. Without that command, notifications are off and jailoc warns |
| `"osc"` | An OSC 777 `notify` sequence written to the terminal, inside a tmux passthrough sequence when `TMUX` is set. Only terminals that implement OSC 777 show it |
| `"off"` | None |

Notifications are sent only when all of these hold:

- The agent is `opencode`, `opencode1`, `opencode2` or a custom agent derived from one of them; their [TUI plugin](../how-to/tui-plugin.md#get-notifications-from-opencode) reports the agent's state. Other agents send none.
- The session is attached interactively: standard input and output are terminals.
- The terminal reported through focus reporting (DEC mode 1004) that it lost focus, and has not regained it before the notification goes out. While the focus is unknown, such as in a terminal without focus reporting, nothing is sent.

Content and timing:

- The title is `jailoc / <workspace>` and the body is either `The agent finished.` or `The agent needs your input.`; neither contains session content.
- jailoc sends at most one notification every 3 seconds and queues each kind at most once.
- A native notification command gets 5 seconds.
- A terminal notification is written between two chunks of agent output and only when it does not wait: when the terminal is not reading, agent output is being written or it ends inside an escape sequence, the notification is dropped, not kept for later.
- After the agent exits, queued notifications are sent for at most 2 seconds; detaching, a takeover from another terminal or a lost connection drops them.
- Dropped and failed notifications are recorded in `~/.cache/jailoc/jailoc.log`.

A detected native command does not guarantee a visible notification: macOS notification permissions and Focus settings, or the Linux desktop's notification service and D-Bus session, still control display. A failed native delivery is not retried through OSC.

### `image`, `dockerfile`, `build_context`

- `image` cannot be combined with `dockerfile` or `build_context`, and must not be whitespace-only.
- `dockerfile` accepts absolute paths, `~` paths and `http`/`https` URLs with a non-empty host (`http:///path` is rejected). Relative paths and other schemes are rejected.

### `agent` and `agents`

- `agent` must be `opencode`, `opencode1`, `opencode2`, `claude`, `copilot`, `omo`, `agy` or a custom agent defined in the workspace's or the defaults' `agents`.
- A key of `agents` that is not a built-in agent is a custom agent:
    - its name must match `^[a-z0-9-]+$`;
    - `from`, set in that table or in the defaults' table of the same name, must be a built-in agent (custom agents cannot be derived from custom agents).
- `from` is rejected on built-in agents.
- `version` must be an exact version (`1.2.3`, optionally with a `-prerelease` and `+build` suffix).
- `mounts` follows the rules of [`mounts`](#mounts).
- `profile` must be `false` or a name matching `^[a-z0-9-]+$`; `true` is rejected.

### `env`

Each entry must be `KEY=VALUE` with a non-empty key.

Reserved keys, which jailoc sets itself:

- `JAILOC`, `JAILOC_WORKSPACE`, `NODE_USE_SYSTEM_CA`, `SSH_AUTH_SOCK`
- the agent variables listed in [Coding agents](#coding-agents) for every agent: `OPENCODE_DISABLE_AUTOUPDATE`, `OPENCODE_TUI_CONFIG`, `OPENCODE_CLI_CONFIG_CONTENT`, `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_SANDBOXED`, `DISABLE_AUTOUPDATER`, `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC`, `COPILOT_AUTO_UPDATE`, `PI_SKIP_VERSION_CHECK`, `OMO_RUNTIME`, `AGY_CLI_DISABLE_AUTO_UPDATE`

### `env_file`

- Paths must be absolute or start with `~`, and the files must exist at config load time.
- Format: `KEY=VALUE` lines, `#` comments, empty lines ignored, quoted values allowed, inline comments stripped.
- Values are literal; no host variable expansion.
- Entries are checked against the reserved keys.

### `secrets`

Secrets live under `[defaults.secrets.env.<NAME>]`, `[defaults.secrets.file.<NAME>]`, `[workspaces.<ws>.secrets.env.<NAME>]` or `[workspaces.<ws>.secrets.file.<NAME>]`. The sub-table sets the destination, the field sets the source.

| Field | Type | Description |
|-------|------|-------------|
| `from_env` | string | Host environment variable to read. Must match `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `from_file` | string | Host file to read. Absolute or `~` path. |
| `from_command` | string | Command run on the host with `/bin/sh -c`; its standard output is the value. Must not be blank. |
| `ttl` | string | Duration (`time.ParseDuration`, e.g. `"15m"`, `"1h"`) for which `from_command` output is cached in the host system keychain per workspace. Only valid with `from_command`; must be positive. |

Exactly one of `from_env`, `from_file` and `from_command` must be set.

| Destination | `<NAME>` rule | In the guest |
|-------------|---------------|--------------|
| `secrets.env.<NAME>` | `^[A-Za-z_][A-Za-z0-9_]*$`; not `HOME`, `PATH` or a reserved `env` key | exported as `<NAME>` in session environments |
| `secrets.file.<NAME>` | `^[a-zA-Z0-9_-]+$` | `/run/secrets/<NAME>` |

A name may not appear in both `secrets.env` and `secrets.file` of the same scope.

**Reading sources.** At `jailoc up`, `jailoc reload` and `jailoc add`, jailoc reads every source on the host:

- a `from_env` variable must be set and non-empty;
- a `from_file` path must be a regular file;
- a `from_command` must exit with status 0, print something and finish within two minutes. Commands run with the terminal attached and `JAILOC_WORKSPACE` set to the workspace name.

Env secret values lose trailing `\r` and `\n` characters and must not contain line breaks or NUL.

**Caching with `ttl`.**

- When `ttl` is set on a `from_command` secret, jailoc caches the command output in the host system keychain (macOS login keychain or Linux kernel user keyring) for that duration, keyed by configuration file, workspace, secret destination, secret name and command.
- `jailoc reload --purge-cache` evicts the cached entries and runs the commands again, and `jailoc down --purge` deletes them.
- Both fail when the keychain refuses to delete an entry and keep it recorded, so the next purge retries it.

**Delivery.**

- Values travel to the guest over SSH.
- File secrets are written to `/run/secrets` (tmpfs, mode `0400`, owned by the agent) and env secrets go to the session environment only.
- jailoc never writes them to plain files on host disk.
- `jailoc reload` re-reads and re-pushes secrets and removes files of secrets no longer configured; running sessions keep their old environment.

See the [secrets how-to](../how-to/secrets.md).

---

## Merge Semantics

**`env`**: variables merge in this order, later wins per key:

1. `[defaults].env`
2. `[defaults].env_file` (in list order)
3. workspace `env_file` (in list order)
4. workspace `env`

**`allowed_hosts` and `allowed_networks`**: unions of the defaults and workspace lists, deduplicated in order.

**`mounts`**: merges three layers keyed by destination: built-in default mounts of the workspace's agent, `[defaults].mounts`, workspace `mounts`. The last layer to set a destination wins; an empty host removes it. Built-in default mounts:

| Agent | Destination | Host source | Mode |
|-------|-------------|-------------|------|
| all | `/home/agent/.agents` | `~/.agents` | `ro` |

- The agent's configuration is copied instead of mounted; see [`import_agent_config`](#import_agent_config).
- The OmO plugin (`oh-my-openagent`) of `opencode1` writes its configuration, state and lock files to `~/.omo`, which is a persistent home directory of the workspace. To share the host's `~/.omo` instead, mount it: `[defaults.agents.opencode1] mounts = ["~/.omo:/home/agent/.omo:rw"]`.

**`snapshots`**: merges `[defaults].snapshots` and workspace `snapshots` keyed by destination. `git_config`, `import_agent_config` and `ssh_auth_sock` add lowest-priority snapshots (the [Git configuration files](#git_config), the [agent configuration](#import_agent_config), `/home/agent/.ssh/known_hosts`), which a `snapshots` entry for the same destination replaces or, with an empty host, removes.

**`ports` and `host_ports`**: `ports` merges by host port; the workspace entry wins. `host_ports` merges by guest port; the workspace entry wins.

**`secrets`**: merges by name.

- A workspace secret replaces a default secret with the same name entirely, also across `env` and `file`.
- An inherited default secret can't be unset in a workspace.

**Scalar settings**: `agent`, `ssh_auth_sock`, `gpg_agent`, `git_config`, `import_agent_config`, `cpu`, `memory`, `docker_disk`, `tmp_storage`, `tmp_size`, `tmp_ttl`, `timezone`, `notifications`, `enable_docker`, `protect_git`, `isolated_build`, `ca_bundle`, and `network.ipv6`, `network.filtered_dns` take the workspace value when set, otherwise `[defaults]`, otherwise the built-in default.

**`agents.<name>`**:

- The scalar rule applies to each field of `agents.<name>`; a custom agent then falls back to the tables of the agent it is derived from (see [Custom agents](#custom-agents)).
- `agents.<name>.mounts` merges instead (see [Agent mounts](#agent-mounts)), and `profile` is not inherited from the derived-from agent.

**DNS settings**: an empty workspace `network.dns_upstream` and an omitted `network.dns_blocked_zones` inherit the defaults; a set `dns_blocked_zones` replaces the default list. A set workspace `network.agent_endpoints`, including `[]`, replaces the defaults list.

---

## Persistent state

`~/.cache/jailoc-vm/workspaces/<workspace>/guest/home` keeps the agent's persistent home directories (see [Coding agents](#coding-agents)) and `~/.cache` across restarts.

- `jailoc down --purge` deletes it.
- An agent [`profile`](#agent-profiles) moves the agent's directories to a shared profile or makes them ephemeral.

### Tool caches

Sessions get these variables, so download caches that tools keep outside `~/.cache` by default land in the VM's persistent `~/.cache` (`/home/agent/.cache`) instead of its in-memory overlay:

| Variable | Value | Read by |
|----------|-------|---------|
| `GOMODCACHE` | `~/.cache/go-mod` | Go module cache (default `~/go/pkg/mod`) |
| `NPM_CONFIG_CACHE` | `~/.cache/npm` | npm (default `~/.npm`) |
| `BUN_INSTALL_CACHE_DIR` | `~/.cache/bun` | Bun install cache (default `~/.bun/install/cache`) |
| `NUGET_PACKAGES` | `~/.cache/nuget/packages` | NuGet global packages (default `~/.nuget/packages`) |
| `NUGET_HTTP_CACHE_PATH` | `~/.cache/nuget/v3-cache` | NuGet HTTP cache (default `~/.local/share/NuGet/v3-cache`) |

Precedence:

- A variable the image sets is kept. For npm, which reads its variables case-insensitively, either `NPM_CONFIG_CACHE` or `npm_config_cache` counts.
- A variable set by the workspace's `env`, `env_file` or an env secret takes precedence. When both npm spellings occur in the merged environment delivered to the guest, the last entry wins.
- The variables also take precedence over the tools' own configuration files, such as a `cache` setting in `.npmrc` or `bunfig.toml`; set the variable in the workspace's `env` to choose another location.

---

See the [workspace configuration how-to](../how-to/workspace-configuration.md) for step-by-step setup.
