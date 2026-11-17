# jailoc 2.7: control the request, not just the host

*November 2026 · release 2.7*

Since 2.4, the network broker decides per host: either the agent can reach `api.github.com` or it can't. That's a blunt tool. Letting an agent read your organization's repositories on GitHub also lets it delete them, if its token allows it. jailoc 2.7 adds a second question for the requests the broker inspects: which method and which path. It narrows what inspected requests can do. It isn't a complete host-level control, and the [warning below](#method-and-path-rules) says where it stops applying.

This post covers HTTP method and path rules, the TLS inspection they need, private DNS answers (`internal_zones`), and a few smaller fixes. It ends with what the new rules don't protect against, and what to do when you upgrade.

**TL;DR**

- A rule with an `http` table decides **per request** by method and path instead of per connection.
- Connections selected for HTTP inspection are decrypted by the broker with a per-workspace CA. Other connections stay encrypted end to end.
- Hostnames resolve to private addresses only inside zones you list in `internal_zones`.
- Sessions keep Go module, npm, Bun and NuGet download caches in the persistent home.
- Restart each workspace once after upgrading. The new broker, runtime and session environment come with the new VM.

## Method and path rules

Add an `http` table to a rule:

```toml
[[workspaces.api.network.rules]]
name = "github-readonly"
allow = ["api.github.com"]
http.methods = ["GET", "HEAD"]
http.paths = ["/repos/seznam/**"]
```

For inspected requests, `api.github.com` answers `GET` and `HEAD` under `/repos/seznam`. An inspected `DELETE`, or a `GET /user`, gets `403` with `blocked by network policy (reason=http-no-match)`. Omit `methods` or `paths` to match any.

!!! warning
    HTTP rules decide only the requests the broker inspects. A broad plain `allow` on the same host and port, such as `dev-deps` or `*`, can leave a connection uninspected, and requests on it bypass `methods` and `paths`:

    - An alternate allowed SNI outside the scope of every matching `http` rule can leave a connection uninspected. A connection without SNI can remain uninspected only where no address-scoped `http` rule (an IP or CIDR target, or `*`) applies and the plain rules permit it.
    - Missing ALPN isn't a bypass: a connection selected for inspection is decrypted and its first line checked. An ALPN list without `h2` or `http/1.1` is decided by plain rules first; if allowed and covered by an `http` rule, it is also decrypted and checked for HTTP.

    Avoid broad plain access to hosts whose methods and paths you rely on `http` rules to restrict. That lowers the risk but doesn't promise read-only containment. `http` rules control inspected requests; they aren't complete host security.

Path patterns are split at `/`. A literal matches that segment, `*` matches exactly one non-empty segment, and `**` matches any number of remaining segments but only as the last one. So `/repos/seznam/*` matches `/repos/seznam/jailoc` and nothing deeper, while `/repos/seznam/**` also covers `/repos/seznam/jailoc/issues`. Patterns are matched case-sensitively against the path without the query string. Ambiguous paths such as `..` segments, `//` or encoded backslashes are denied as `bad-path` where a path pattern applies; the [reference](../reference/configuration.md#request-path-checking) has the exact rules.

Two behaviors are easy to trip over:

- **An `http` allow rule narrows its host.** Within an inspected connection, a plain rule such as `dev-deps` that also allows `api.github.com` on the same port grants nothing for that host once an `http` allow rule matches it. Only the `http` allow rules decide its requests.
- **A `deny` with `http` is per request.** It acts on inspected requests, and never blocks DNS or the connection. It also doesn't open anything, so a host you want to restrict still needs an allow rule:

```toml
[[workspaces.api.network.rules]]
name = "registry"
allow = ["registry.example.com"]

[[workspaces.api.network.rules]]
name = "registry-no-writes"
deny = ["registry.example.com"]
http.methods = ["DELETE", "POST", "PUT", "PATCH"]
```

Inspected reads pass through the plain `registry` rule. An inspected `PUT` gets `403` with `blocked by network policy (rule=registry-no-writes)`. Without the first rule the host would be default-denied altogether.

`jailoc traffic -f` shows one line per inspected request with method, host, path, status and the rule that decided. See [Restrict HTTP methods and paths](../how-to/network-access.md#restrict-http-methods-and-paths) for the full recipe, including how to loosen one host under a broad rule.

## How the broker reads HTTPS

To see a request, the broker has to terminate TLS. It does that only for connections selected by a matching `http` rule. If the TLS handshake negotiates `h2` or `http/1.1`, the broker serves HTTP. Without a negotiated HTTP protocol, it checks the first line instead. A ClientHello that offers only non-HTTP ALPN protocols needs a plain allow rule before inspection; an `http` rule alone doesn't open it. Allowed connections outside the inspection scope stay opaque, as in 2.4.

For inspected connections the broker presents certificates signed by a per-workspace CA, created on the host the first time a rule has `http`. The CA key stays in the workspace's state directory on the host with mode `0600`. It never enters the VM. The CA certificate does: the guest trusts it, even with `ca_bundle = false`, so guest root and the agent trust it too. Treat that state directory like the rest of the VM's host-side files.

The broker connects to the real server over a second TLS connection and verifies the upstream certificate against the host's system roots plus the `ca_bundle` file when you set a path. A failure ends the request with `502`. The broker sees full requests and responses in memory, but logs only method, host, path (no query string) and status to `broker.jsonl`, never bodies.

[Network isolation](../explanation/network-isolation.md#tls-inspection) explains protocol detection, HTTP/2 and cleartext HTTP.

## What this doesn't protect against

An `http` rule narrows what the agent can do in inspected HTTP requests. It isn't a defense against exfiltration, and the warning above lists connections that skip inspection.

- A permitted `GET` still carries data in its path and query string. If the agent may read `/repos/seznam/**`, it can encode a secret into a request there, and the query string isn't checked.
- A WebSocket upgrade is checked like any request, but after the `101` response the connection passes through unfiltered. Frames aren't inspected.
- `isolated_build` workspaces don't inspect HTTPS during the image build. Their `allow` rules with `http` open the host and port for every method and path, and `deny` rules with `http` don't apply. jailoc prints a warning.
- Clients that pin certificates or keys, keep their own CA store (certifi, a Java keystore, Firefox NSS) or use mutual TLS fail the handshake on inspected hosts. `jailoc traffic` shows an `inspect-error` with a hint. Trust the CA in that client, or remove the `http` table from the matching rules. See [Troubleshooting](../how-to/troubleshooting.md#certificate-error-on-an-inspected-host).
- The broker doesn't chain through an upstream proxy ([#398](https://github.com/seznam/jailoc/issues/398)), so `HTTP_PROXY` isn't used for upstream connections.

If an agent shouldn't be able to send data out, keep it off the host entirely. Use `http` rules to cut what inspected requests to a trusted host can do.

## Private DNS answers need `internal_zones`

In 2.4, an allowed hostname could resolve to a private address and open it. That is DNS rebinding territory: whoever runs DNS for an allowed name picks the address, so an allowed `*.nip.io` name like `10-0-0-5.nip.io` would reach `10.0.0.5`.

2.7 makes that trust explicit, per DNS zone. The broker drops `A` and `AAAA` answers with a private address unless the queried name, the record owner and every `CNAME` in the answer are in an internal zone:

```toml
[defaults.network]
internal_zones = ["corp.example.com"]

[[workspaces.work.network.rules]]
name = "corp"
allow = ["gitlab.corp.example.com", "*.registry.corp.example.com"]
```

A zone covers itself and everything under it. It only says whose DNS you trust, and opens nothing alone: the names still need an `allow` rule, and `deny` rules still win. Zones from `[defaults.network]` and the workspace are merged. A public name with a `CNAME` into `corp.example.com`, or a chain through any public name, is dropped. `jailoc traffic` shows `dropped <address>: private address outside internal_zones`.

jailoc rejects zones that would defeat the point: `*`, public suffixes (`com`, `co.uk`, `github.io`) and wildcard resolvers such as `nip.io` and `sslip.io`. Names without a public registry (`corp`, `lan`, `home.arpa`) are fine.

Existing `allowed_hosts` entries that are valid zones are trusted as internal zones in memory, and `jailoc migrate` writes them to `internal_zones`. Entries that are rejected as zones, such as public suffixes and wildcard resolvers, aren't trusted. Two side effects: jailoc no longer resolves these hostnames on the host, and a name that is a `CNAME` into a zone you didn't list stops resolving to its private address. `jailoc up`, `reload` and `migrate` warn about that `CNAME` case; add the canonical name's zone.

The corporate example in the [2.4 post](network-rules.md) needs one extra line, the `internal_zones` above, to keep reaching private addresses.

## Smaller changes

- **Clearer denials.** A flow denied after SNI or `Host` inspection gets a TLS `access_denied` alert for HTTPS or a `403` with the rule name for plain HTTP, so clients report a policy denial instead of a connection reset.
- **Quieter `jailoc traffic`.** `log = false` on an `allow` or `deny` rule hides the events that rule decides from `jailoc traffic` unless you pass `--all`. The rule still applies and the broker still logs its events. See [Quiet rules](../reference/configuration.md#quiet-rules).
- **Download caches survive restarts.** Sessions set `GOMODCACHE`, `NPM_CONFIG_CACHE`, `BUN_INSTALL_CACHE_DIR`, `NUGET_PACKAGES` and `NUGET_HTTP_CACHE_PATH` into the persistent `~/.cache`, instead of the in-memory overlay. A variable set by the image or by your workspace `env` wins. These variables also take precedence over `.npmrc` or `bunfig.toml` settings. jailoc sets only these five and doesn't redirect other tools' caches. See [Tool caches](../reference/configuration.md#tool-caches) and [Resource use](../explanation/resource-use.md#what-counts-against-memory).
- **Claude Code background sessions.** jailoc sets `CLAUDE_CODE_SANDBOXED=1`, so Claude Code trusts the working directory without its prompt, and sets `bypassPermissionsModeAccepted` in `~/.claude/.claude.json` when the file doesn't. Background sessions (`claude --bg`, used when switching sessions) can start without an interactive step. The state file is now replaced through a temporary file.
- **Agent config from a dotfiles repository.** A read-only mount whose host directory is missing fails `jailoc up` instead of sharing an empty directory, and `jailoc up` warns about symlinks that dangle in the VM. See [Use agent configuration from a dotfiles repository](../how-to/coding-agents.md#use-agent-configuration-from-a-dotfiles-repository).
- **macOS virtio-fs stall.** Unlinking a FIFO without a peer could block the virtio-fs worker. The bundled libkrun carries a patch that opens such inodes non-blocking. It applies to macOS only.

For contributors: the [dev Dockerfile](https://github.com/seznam/jailoc/blob/main/dev/Dockerfile.jailoc) already installs a checksum-verified `golangci-lint` v2.15.0, matching CI. That isn't new in 2.7.

## Upgrading

1. Update jailoc, then run `jailoc restart <workspace>` for each workspace. The restart ends running sessions. It brings the new broker, runtime and session environment (cache variables, Claude Code settings). If the running broker can't apply the network rules you configured, `jailoc reload` and `jailoc up` refuse to update the policy and ask for a restart.
2. Check `jailoc traffic` for `dropped ... private address outside internal_zones` lines. Add zones for internal names you trust, or run `jailoc migrate` if your config still uses `allowed_hosts`.
3. After that, edits to `http` rules apply with `jailoc reload`. New connections follow them. Established connections, including later requests on them, keep the policy they were opened under, so restart the workspace to end that access immediately.
4. Use `jailoc traffic -f` to see what each inspected request hit before you tighten a rule.

Read more: [Configure network access](../how-to/network-access.md), [Network isolation](../explanation/network-isolation.md), [HTTP rules](../reference/configuration.md#http-rules) and [Internal zones](../reference/configuration.md#internal-zones) in the configuration reference.
