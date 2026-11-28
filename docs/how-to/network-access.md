# How to configure network access

All guest traffic leaves through jailoc's network broker on the host. Add rules to `~/.config/jailoc/config.toml`, apply them with `jailoc reload <workspace>` and check the result with `jailoc traffic -f`. For the model behind the rules, see [Network isolation](../explanation/network-isolation.md).

Defaults: a destination that no allow rule matches is denied (`default-deny`). The coding agent reaches its model and authentication endpoints through the `agent` rule: Claude Code, Copilot CLI and Antigravity with their built-in endpoints, OpenCode and OmO with the hosts in `agent_endpoints`. New configurations set `agent_endpoints` in `[defaults.network]` and include a `dev-deps` rule in `[defaults.network.rules]` that lists common package registries, GitHub, container registries and OS mirrors. A `deny` rule always wins over an `allow` rule, whatever the order; rules from `[defaults.network.rules]` and the workspace form one set.

## Allow or deny hostnames

Add `[[workspaces.<name>.network.rules]]` entries with `allow` or `deny`, then run `jailoc reload api`:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]

[[workspaces.api.network.rules]]
name = "gitlab"
allow = ["gitlab.example.com", "*.internal.example.com"]

[[workspaces.api.network.rules]]
name = "no-paste"
deny = ["pastebin.com", "gist.github.com", "*.ngrok.io"]
```

`jailoc traffic -f` names the deciding rule (`rule=gitlab`, `rule=no-paste`). The broker enforces hostname rules at DNS resolution, at connection time and on the TLS SNI or HTTP `Host` header; see [Enforcement by name](../explanation/network-isolation.md#enforcement-by-name). A denial looks like this to the agent:

| Where | Result |
|-------|--------|
| DNS lookup of a denied name | `NXDOMAIN` |
| Allowed address, denied name in the SNI or `Host` header (for example a CDN address shared with an allowed site), HTTPS | `tlsv1 alert access denied` |
| Same, plain HTTP | `403 Forbidden` that names the rule |

## Reach internal hosts by name

A hostname rule reaches a private address (for example `10.20.5.10`) only when the name is in an internal zone. List the DNS zones you control in `internal_zones`, allow the hosts and run `jailoc reload api`:

```toml
[defaults.network]
internal_zones = ["internal.example.com"]

[[workspaces.api.network.rules]]
name = "gitlab"
allow = ["gitlab.internal.example.com"]
```

A zone covers its subdomains. For any other name, the broker drops answers that point to a private address, so a public name can't be rebound to your network. An answer is also dropped when a `CNAME` in its chain is outside the internal zones. `jailoc traffic` shows dropped answers as `dropped <address>: private address outside internal_zones`; add the zone if you trust its DNS. Public suffixes such as `com` or `github.io` and wildcard DNS services such as `nip.io` are rejected as zones. See [Internal zones](../reference/configuration.md#internal-zones).

## Allow a private IP or CIDR range

To open a private or reserved address or subnet directly by IP (without DNS resolution), add an IP or CIDR rule:

```toml
[[workspaces.api.network.rules]]
name = "db"
allow = ["10.20.5.10"]
proto = "tcp"
ports = [5432]

[[workspaces.api.network.rules]]
name = "internal-dev"
allow = ["10.20.0.0/16", "fd12:3456:789a::/48"]
ports = [9090, "8000-8100"]
```

`proto` is `"tcp"` (default), `"udp"` or `"any"`. `ports` takes integers or `"a-b"` ranges and defaults to all ports.

## Allow the public internet

To opt a workspace into all public destinations:

```toml
[[workspaces.open.network.rules]]
name = "internet"
allow = ["*"]
```

`*` matches any public address on the rule's protocol. It never opens private or reserved ranges, and it excludes DoT port 853 and QUIC UDP port 443 unless you list them explicitly in `ports`.

## Limit the agent's endpoints

The `agent` rule allows the coding agent's model and auth endpoints on TCP 443. Claude Code, Copilot CLI and Antigravity get their built-in endpoints. OpenCode and OmO support several LLM providers, so for them the rule allows the hosts in `agent_endpoints`. The setup wizard and `jailoc migrate` ask which providers you use (all selected by default) and write their endpoints. To change the list later, edit it and run `jailoc reload api`:

```toml
[defaults.network]
agent_endpoints = ["api.anthropic.com", "models.dev"]
```

`jailoc traffic` then shows connections to the other providers as denied. A workspace's `[workspaces.<name>.network]` `agent_endpoints` replaces the defaults list. Without `agent_endpoints`, or with `[]`, an OpenCode or OmO workspace has no `agent` rule, so the agent needs your own allow rules. To block a provider of Claude Code, Copilot CLI or Antigravity, add a `deny` rule. See [`rule=agent`](../reference/configuration.md#ruleagent) for each agent's endpoints.

## Restrict HTTP methods and paths

Add an `http` table to a rule to decide per request instead of per connection. This allows read-only access to one organization on GitHub:

```toml
[[workspaces.api.network.rules]]
name = "github-readonly"
allow = ["api.github.com"]
http.methods = ["GET", "HEAD"]
http.paths = ["/repos/seznam/*"]
```

Run `jailoc reload api` to apply the change. Other methods or paths on `api.github.com` get `403` with `blocked by network policy (reason=http-no-match)`. A plain `allow` rule for the same host and port doesn't override it: once an `http` allow rule matches a host, only `http` allow rules decide its requests. Omit `methods` or `paths` to match any; `http` works with `proto = "tcp"` only. In a path, `*` stands for exactly one segment and `**` for any number of trailing segments. See [HTTP rules](../reference/configuration.md#http-rules) for the full pattern grammar and every outcome.

!!! warning
    Rules with `http` make the broker decrypt TLS for the matching hosts, and the guest trusts a workspace CA for that. See [TLS inspection](../explanation/network-isolation.md#tls-inspection).

### Block methods instead

A `deny` rule with `http` acts per request and leaves other requests to the allow rules:

```toml
[[workspaces.api.network.rules]]
name = "no-writes"
deny = ["registry.example.com"]
http.methods = ["DELETE", "POST", "PUT", "PATCH"]
```

A denied request gets `403` with `blocked by network policy (rule=no-writes)`. DNS and the connection itself stay open.

### Loosen one host under a broad rule

Add a more permissive `http` allow rule for the host. Any matching `http` allow rule lets a request through:

```toml
[[workspaces.api.network.rules]]
name = "web-readonly"
allow = ["*"]
http.methods = ["GET", "HEAD"]

[[workspaces.api.network.rules]]
name = "registry-full"
allow = ["registry.example.com"]
http.methods = ["*"]
```

!!! warning
    `http` allow rules are a union across names and addresses. A `GET`-only `http` allow rule on an IP or CIDR is not a ceiling when the request presents a name that another `http` allow rule (for example `POST`) matches: the request can use the named rule's methods and paths even if that name resolves elsewhere. Use an `http` `deny` rule as a hard cap. See [Evaluation](../reference/configuration.md#evaluation).

### Fix certificate errors on inspected hosts

A client that pins the certificate, reads its own CA store (certifi, a Java keystore, Firefox NSS) or needs mTLS rejects the broker's certificate, and `jailoc traffic` shows an `inspect-error` line with a hint. Trust the CA from the guest's `/etc/ssl/certs/ca-certificates.crt` in that client, or remove the `http` table from every rule that matches the host to pass it through uninspected. See [Troubleshooting](troubleshooting.md#certificate-error-on-an-inspected-host).

## Configure upstream DNS and default rules

Set an optional upstream DNS resolver with `dns` and shared rules under `[defaults.network]`:

```toml
[defaults.network]
dns = "9.9.9.9"

[[defaults.network.rules]]
name = "dev-deps"
allow = ["github.com", "api.github.com", "*.githubusercontent.com", "registry.npmjs.org", "pypi.org", "files.pythonhosted.org", "proxy.golang.org", "sum.golang.org"]
```

## Query another DNS server directly

The guest resolves through the gateway (`192.168.127.1`), and port 53 to any other server is denied (`dns-filtered`). To let tools such as `dig @10.33.4.25` query a specific resolver, allow it with an explicit port 53 and run `jailoc reload api`:

```toml
[[workspaces.api.network.rules]]
name = "coredns"
allow = ["10.33.4.25", "coredns-lb.example.internal"]
proto = "any"
ports = [53]
```

`allow = ["*"]` with `ports = [53]` opens every public resolver. A rule without `ports` never opens port 53. The broker answers these queries itself with the same name filtering as the gateway resolver, but it doesn't remember their answers, so connecting to an address learned this way needs an IP or CIDR rule, or the same name resolved through the gateway.

## See what the agent connects to

```bash
jailoc traffic -f
```

Each line shows a connection with the DNS name, the verdict and the rule that decided it (`rule=no-paste`, `rule=dev-deps`, `rule=agent`, or `default-deny`). Requests to hosts covered by an [`http` rule](#restrict-http-methods-and-paths) get a line each with method, host, path, status and the deciding rule; denied ones show the reason. A request that never reaches the upstream, for example because its certificate failed verification, shows status `502` and the error. See [`jailoc traffic`](../reference/cli.md#jailoc-traffic).

### Hide noisy rules

Set `log = false` on a rule whose events bury the rest, for example a package registry or a telemetry host you deny:

```toml
[[workspaces.api.network.rules]]
name = "registry"
allow = ["registry.npmjs.org"]
log = false

[[workspaces.api.network.rules]]
name = "no-telemetry"
deny = ["telemetry.example.com"]
log = false
```

`jailoc traffic` hides what these rules decide. The rules still apply, and `jailoc traffic --all` shows their events. See [Quiet rules](../reference/configuration.md#quiet-rules).

## Reach IPv6 services

IPv6 is off by default: the guest has IPv4 only and the broker denies every IPv6 flow. Turn it on per workspace or in `[defaults.network]`, then run `jailoc restart api`:

```toml
[workspaces.api.network]
ipv6 = true
```

The guest then has IPv6 (`fd00:127::2/64`). Global IPv6 addresses follow the same allow and deny rules as public IPv4 ones. Unique local addresses (`fc00::/7`) count as private and require an explicit IPv6 CIDR rule.

!!! warning
    Some ranges stay blocked whatever you allow: loopback, link-local, multicast, documentation and other reserved IPv6 ranges, Teredo, cloud metadata endpoints, host addresses and the broker gateway. To reach a service on the host, use [`host_ports`](workspace-configuration.md#reach-host-services). IPv4 embedded in IPv6 (IPv4-mapped, NAT64 `64:ff9b::/96`, 6to4) follows the IPv4 rules for the embedded address.

## Apply changes to a running workspace

```bash
jailoc reload api
```

New connections and DNS lookups follow the updated rules immediately. Running sessions keep going.

## Allow an HTTP proxy on a private network

If your traffic goes through a proxy on a private address, allow the proxy's IP or CIDR and pass its URL to sessions:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
env = [
  "HTTP_PROXY=http://10.20.0.50:3128",
  "HTTPS_PROXY=http://10.20.0.50:3128",
]

[[workspaces.api.network.rules]]
name = "proxy"
allow = ["10.20.0.50"]
ports = [3128]
```

!!! note
    Image builds get the host's proxy variables as build arguments automatically. Only sessions need the `env` entries.

## Chain egress through an upstream proxy

To send every allowed TCP flow through a proxy on the host side instead of dialing it from the broker, add an `upstream_proxy` table. The broker keeps deciding with your rules first; the proxy only receives flows they allowed.

!!! warning
    The proxy gets the hostname (`CONNECT registry.npmjs.org:443`) and resolves it itself, so jailoc can't pin the address the tunnel ends at. The proxy is trusted to refuse private and internal final addresses; the broker's private-range denial and `internal_zones` don't apply past it. Use a proxy that enforces this.

Static proxy, with Basic auth and one host dialed directly:

```toml
[defaults.network.upstream_proxy]
url    = "http://proxy.example.com:3128"
ca     = "~/certs/proxy-ca.pem"   # only for a proxy that re-signs TLS
direct = ["git.example.com:22"]  # allowed by a rule, dialed without the proxy

[defaults.network.upstream_proxy.auth]
username     = "jdoe"
from_command = "pass show proxy"
ttl          = "8h"
```

Proxy per workspace, from a host helper that runs in the broker when the workspace starts:

```toml
[defaults.network.upstream_proxy]
command = 'my-egress-helper resolve --project "$JAILOC_WORKSPACE"'
```

The helper prints JSON with `version` and `url` and optional `ca`, `direct` and `headers`; see the [helper contract](../reference/configuration.md#helper-contract). A helper failure at start stops the VM from booting and jailoc prints its stderr. Turn the proxy off for one workspace:

```toml
[workspaces.offline-lab.network.upstream_proxy]
enabled = false
```

Then run `jailoc reload api`. Notes:

- A workspace `upstream_proxy` table replaces the defaults table entirely.
- DNS and SNTP stay local; other UDP is denied unless `direct` lists it, and QUIC falls back to TCP.
- Don't add the proxy's address to `network.rules`: the broker reaches it by itself, without opening that address to the guest.
- With `from_env` auth, the variable is read when `jailoc up` starts the broker; change it, then run `jailoc restart`.
- The `ca` file is also added to the guest trust store, also with `ca_bundle = false`. Running processes may need a restart to see it.
- Image builds with the host Docker daemon don't use the proxy.

See [`[defaults.network.upstream_proxy]`](../reference/configuration.md#defaultsnetworkupstream_proxy) for every field.

## Allow local-network hosts on macOS

On macOS, reaching hosts on your local network needs the macOS Local Network permission for the process that runs jailoc (usually your terminal). Without it, connections to those addresses fail with `no route to host` even when they're allowed.

## Forward a CA bundle

If your host trusts a private CA (a corporate TLS proxy or an internal registry), the guest needs the same trust. By default, `ca_bundle` is unset, which means automatic discovery: jailoc forwards the bundle named by `SSL_CERT_FILE`, falling back to `NIX_SSL_CERT_FILE`. Nothing is forwarded when neither is set. Point at a specific bundle, or turn forwarding off, then run `jailoc reload`:

```toml
[defaults]
ca_bundle = "~/certs/corp-ca-bundle.pem"

[workspaces.public-only]
paths = ["/home/you/projects/public-only"]
ca_bundle = false
```

`ca_bundle = true` requests automatic discovery explicitly. The bundle is added to the guest's system trust store, and sessions get `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE` and similar variables pointing at it, so Node.js, Python, curl, git and the coding agents trust it too; see [`ca_bundle`](../reference/configuration.md#ca_bundle) for the full list. A reload updates the store live, but processes that already loaded it, including the guest Docker daemon, keep the old one until they restart, and running sessions keep their old environment.

The forwarded bundle also reaches the image builds, which then run in a builder VM; see [Build with a private CA](custom-images.md#build-with-a-private-ca). A changed bundle reaches the image only after `jailoc restart`.

!!! note
    If the selected variable points at an invalid PEM bundle, `jailoc up` fails instead of falling through to the next variable. Set `ca_bundle = false` or a valid path to fix it.

!!! warning
    A CA bundle doesn't open network access. Endpoints still need a matching allow rule in `network.rules`.
