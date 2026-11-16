# How to configure network access

All guest traffic leaves through jailoc's network broker on the host. By default, destinations not matched by an allow rule are denied, except for the configured coding agent's API and authentication endpoints (`rule=agent`). New configurations include a `dev-deps` rule in `[defaults.network.rules]` listing common package registries, GitHub, container registries and OS mirrors directly in `config.toml`. For the model behind it, see [Network isolation](../explanation/network-isolation.md).

## Allow or deny hostnames

Add `[[workspaces.<name>.network.rules]]` entries with `allow` or `deny`:

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

Rules from `[defaults.network.rules]` and the workspace are combined into one set. Deny rules always win over allow rules regardless of order.

The broker enforces hostname rules at DNS resolution, at connection time (using its TTL-bounded IP-to-name cache), and by inspecting the TLS ClientHello SNI or HTTP `Host` header before dialing upstream.

A denied name gets NXDOMAIN at DNS. When the agent reaches an allowed address with a denied name in the SNI or `Host` header, as on a CDN address shared with an allowed site, the client reports `tlsv1 alert access denied` for HTTPS or a `403 Forbidden` that names the rule for plain HTTP.

## Reach internal hosts by name

A hostname rule reaches a private address (for example `10.20.5.10`) only when the name is in an internal zone. List the DNS zones you control in `internal_zones`, then allow the hosts:

```toml
[defaults.network]
internal_zones = ["internal.example.com"]

[[workspaces.api.network.rules]]
name = "gitlab"
allow = ["gitlab.internal.example.com"]
```

A zone covers its subdomains. For any other name, the broker drops answers that point to a private address, so a public name can't be rebound to your network. `jailoc traffic` shows them as `dropped <address>: private address outside internal_zones`; add the zone if you trust its DNS. Public suffixes such as `com` or `github.io` and wildcard DNS services such as `nip.io` are rejected. See [Internal zones](../reference/configuration.md#internal-zones).

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

`proto` defaults to `"tcp"` (`"tcp"`, `"udp"`, or `"any"`). `ports` accepts integers or `"a-b"` ranges and defaults to all ports.

## Allow the public internet

To opt a workspace into all public destinations:

```toml
[[workspaces.open.network.rules]]
name = "internet"
allow = ["*"]
```

`*` matches any public address on the rule's protocol (excluding DoT port 853 and QUIC UDP port 443 unless listed explicitly in `ports`). It never opens private or reserved ranges.

## Restrict HTTP methods and paths

Add an `http` table to a rule to decide per request instead of per connection. This allows read-only access to one organization on GitHub:

```toml
[[workspaces.api.network.rules]]
name = "github-readonly"
allow = ["api.github.com"]
http.methods = ["GET", "HEAD"]
http.paths = ["/repos/seznam/*"]
```

Other methods or paths on `api.github.com` get `403` with `blocked by network policy (reason=http-no-match)`. A plain `allow` rule for the same host and port doesn't override it: once an `http` allow rule matches a host, only `http` allow rules decide its requests. Omit `methods` or `paths` to match any. In a path, `*` stands for exactly one segment and `**` for any number of trailing segments (`/repos/seznam/**`).

To block methods instead, use a `deny` rule. It acts per request and leaves other requests to the allow rules:

```toml
[[workspaces.api.network.rules]]
name = "no-writes"
deny = ["registry.example.com"]
http.methods = ["DELETE", "POST", "PUT", "PATCH"]
```

A denied request gets `403` with `blocked by network policy (rule=no-writes)`. DNS and the connection itself stay open.

To loosen one host under a broad `http` rule, add a more permissive `http` allow rule for it. Any matching `http` allow rule lets a request through:

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

To list the requests, run `jailoc traffic -f`. Inspected requests show method, host, path, status and the deciding rule; denied ones show the reason. A request that never reaches the upstream, for example because its certificate failed verification, shows status `502` and the error.

`http` works with `proto = "tcp"` only. Run `jailoc reload api` to apply a change. See [HTTP rules](../reference/configuration.md#http-rules) for the pattern grammar and every outcome.

!!! warning
    Rules with `http` make the broker decrypt TLS for the matching hosts, and the guest trusts a workspace CA for that. See [TLS inspection](../explanation/network-isolation.md#tls-inspection).

### Fix certificate errors on inspected hosts

A client that rejects the broker's certificate for an inspected host fails with a certificate error, and `jailoc traffic` shows an `inspect-error` line with a hint. The client doesn't trust the workspace CA. The usual causes are:

- the client pins the server certificate or key;
- the client reads its own CA store instead of the system one (certifi, a Java keystore, Firefox NSS);
- the server requires a client certificate (mTLS).

Trust the CA in that client's store, or pass the host through uninspected by removing the `http` table from every rule that matches it. The CA certificate is in the guest's `/etc/ssl/certs/ca-certificates.crt`. See [Troubleshooting](troubleshooting.md#certificate-error-on-an-inspected-host).

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

The guest resolves through the gateway (`192.168.127.1`), and port 53 to any other server is denied (`dns-filtered`). To let tools such as `dig @10.33.4.25` query a specific resolver, allow it with an explicit port 53:

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

Each line shows a connection with the DNS name, verdict, and the rule that decided it (`rule=no-paste`, `rule=dev-deps`, `rule=agent`, or `default-deny`). Requests to hosts covered by an [`http` rule](#restrict-http-methods-and-paths) get a line each. See [`jailoc traffic`](../reference/cli.md#jailoc-traffic).

## Reach IPv6 services

IPv6 is off by default: the guest has IPv4 only and the broker denies every IPv6 flow. Turn it on per workspace or in `[defaults.network]`:

```toml
[workspaces.api.network]
ipv6 = true
```

Run `jailoc restart api` to apply it. The guest then has IPv6 (`fd00:127::2/64`). Global IPv6 addresses follow the same allow and deny rules as public IPv4 ones. Unique local addresses (`fc00::/7`) count as private and require an explicit IPv6 CIDR rule.

!!! warning
    Some ranges stay blocked whatever you allow: loopback, link-local, multicast, documentation and other reserved IPv6 ranges, Teredo, cloud metadata endpoints, host addresses and the broker gateway. To reach a service on the host, use [`host_ports`](workspace-configuration.md#reach-host-services). IPv4 embedded in IPv6 (IPv4-mapped, NAT64 `64:ff9b::/96`, 6to4) follows the IPv4 rules for the embedded address.

## Apply changes to a running workspace

```bash
jailoc reload api
```

New connections and DNS lookups follow the updated rules immediately. Running sessions keep going.

## Allow an HTTP proxy on a private network

If your traffic goes through a proxy on a private address, allow the proxy's IP/CIDR and pass its URL to sessions:

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

## Allow local-network hosts on macOS

On macOS, reaching hosts on your local network needs the macOS Local Network permission for the process that runs jailoc (usually your terminal). Without it, connections to those addresses fail with `no route to host` even when they're allowed.

## Forward a CA bundle

If your host trusts a private CA (a corporate TLS proxy or an internal registry), the guest needs the same trust. By default, `ca_bundle` is unset, which means automatic discovery: jailoc forwards the bundle named by `SSL_CERT_FILE`, falling back to `NIX_SSL_CERT_FILE`. Nothing is forwarded when neither is set.

Point at a specific bundle, or turn forwarding off:

```toml
[defaults]
ca_bundle = "~/certs/corp-ca-bundle.pem"

[workspaces.public-only]
paths = ["/home/you/projects/public-only"]
ca_bundle = false
```

`ca_bundle = true` requests automatic discovery explicitly. The bundle is added to the guest's system trust store, and sessions get `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE` and similar variables pointing at it, so Node.js, Python, curl, git and the coding agents trust it too; see [`ca_bundle`](../reference/configuration.md#ca_bundle) for the full list. `jailoc reload` updates the store live, but processes that already loaded it, including the guest Docker daemon, keep the old one until they restart, and running sessions keep their old environment.

!!! note
    If the selected variable points at an invalid PEM bundle, `jailoc up` fails instead of falling through to the next variable. Set `ca_bundle = false` or a valid path to fix it.

!!! warning
    A CA bundle doesn't open network access. Endpoints still need a matching allow rule in `network.rules`.
