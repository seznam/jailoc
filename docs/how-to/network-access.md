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

## Configure upstream DNS and default rules

Set an optional upstream DNS resolver with `dns` and shared rules under `[defaults.network]`:

```toml
[defaults.network]
dns = "9.9.9.9"

[[defaults.network.rules]]
name = "dev-deps"
allow = ["github.com", "api.github.com", "*.githubusercontent.com", "registry.npmjs.org", "pypi.org", "files.pythonhosted.org", "proxy.golang.org", "sum.golang.org"]
```
## See what the agent connects to

```bash
jailoc traffic -f
```

Each line shows a connection with the DNS name, verdict, and the rule that decided it (`rule=no-paste`, `rule=dev-deps`, `rule=agent`, or `default-deny`). See [`jailoc traffic`](../reference/cli.md#jailoc-traffic).

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

`ca_bundle = true` requests automatic discovery explicitly. The bundle is added to the guest's system trust store. `jailoc reload` updates it live, but processes that already loaded the store, including the guest Docker daemon, keep the old one until they restart.

!!! note
    If the selected variable points at an invalid PEM bundle, `jailoc up` fails instead of falling through to the next variable. Set `ca_bundle = false` or a valid path to fix it.

!!! warning
    A CA bundle doesn't open network access. Endpoints still need a matching allow rule in `network.rules`.
