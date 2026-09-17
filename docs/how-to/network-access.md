# How to allow specific hosts or networks

By default, jailoc containers can't reach private addresses. This guide shows how to punch holes in that restriction for specific hostnames or CIDR ranges. For a full explanation of how the network isolation model works, see [Network isolation](../explanation/network-isolation.md).

---

## Allow a specific hostname

Add the hostname to `allowed_hosts` in your workspace config. The name is resolved to an IP address when the container starts, and that address gets an explicit `ACCEPT` rule before the catch-all `DROP`.

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = ["internal-registry.example.com"]
```

You can list multiple hostnames:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = [
  "internal-registry.example.com",
  "internal-mcp.example.com",
]
```

!!! note
    Resolution happens at container start. If the hostname doesn't resolve at that point, the rule won't be added. Dynamic IPs that change after startup are not re-resolved.

---

## Allow a CIDR range

Use `allowed_networks` to permit an entire subnet:

```toml
[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = ["172.20.0.0/16"]
```

Multiple ranges are supported:

```toml
[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = [
  "172.20.0.0/16",
  "10.10.5.0/24",
]
```

Values must be valid CIDR notation. Invalid entries are rejected at config load time.

---

## Combine hosts and networks

Both fields can be set on the same workspace:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.10.5.0/24"]
```

All resolved host IPs and all listed CIDRs get `ACCEPT` rules. Everything else in the RFC 1918, link-local, and CGNAT ranges is blocked.

---

## Allow an HTTP proxy on a private network

If your environment routes traffic through an HTTP proxy that lives on a private address (RFC 1918, link-local, or CGNAT), the container must be able to reach it. Add the proxy's address to `allowed_hosts` or its subnet to `allowed_networks`, and pass the proxy URL via `env`:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = ["proxy.internal.example.com"]
env = [
  "HTTP_PROXY=http://proxy.internal.example.com:3128",
  "HTTPS_PROXY=http://proxy.internal.example.com:3128",
]
```

Without the allowlist entry, the proxy address falls into a blocked range and all proxied requests fail silently.

!!! note
    During image builds, jailoc automatically forwards the host's `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` (and their lowercase variants) as Docker build args. No extra configuration is needed for the build step — only the running container requires explicit `env` entries.

---

## Apply rules to all workspaces

To allow a host or network for every workspace, use the `[defaults]` section instead of repeating it in each workspace:

```toml
[defaults]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8"]
```

Per-workspace rules are merged with defaults — both lists are combined (duplicates removed). Workspace-level rules do not override defaults; they add to them.

---

## Restrict internal DNS names

To route workspace DNS through a filtered resolver, set a numeric upstream address and the DNS zones to reject in your workspace config:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
filtered_dns = true
dns_upstream = "10.20.30.53"
dns_blocked_zones = ["corp.example.com", "internal"]
```

Start the workspace with `jailoc up api`. If it is already running, restart it with `jailoc down api` followed by `jailoc up api` to apply the policy. DNS requests for the blocked suffixes and their subdomains return NXDOMAIN; other DNS requests go to `10.20.30.53`. Docker service names such as `dind` remain resolvable. Set these fields under `[defaults]` to apply the policy to multiple workspaces; a workspace can override the upstream, blocked zones, or `filtered_dns = false` individually.

Do not put a blocked name or one of its subdomains in `allowed_hosts`. jailoc rejects that combination during config validation, including when the host or zone is inherited from `[defaults]`. Remove the host from the blocked zone to permit both name resolution and its firewall rule.

!!! warning
    This filters standard DNS queries only. DNS over HTTPS, proxies, and direct IP addresses bypass name filtering. Keep the private-network firewall enabled, and do not rely on DNS filtering to prevent access to explicitly allowed networks.

See [DNS resolution and filtering](../explanation/network-isolation.md#dns-resolution-and-filtering) for how Docker and the sidecar process lookups, or the [configuration reference](../reference/configuration.md#filtered-dns) for field constraints.

---

## Forward a CA bundle

If your host trusts a private or internal CA (for example, a corporate MITM proxy or an internal registry), the opencode container needs the same trust for HTTPS tools and model-provider connections. When Docker is enabled, the nested Docker daemon (DinD) also needs it for registry operations.

By default, `ca_bundle` is omitted, which enables automatic discovery: jailoc reuses the CA bundle its own process already trusts, taken from the non-empty `SSL_CERT_FILE` environment variable, falling back to `NIX_SSL_CERT_FILE`. No config is required if one of those variables is already set correctly in your shell before running `jailoc up`:

To request automatic discovery explicitly:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
ca_bundle = true
```

To select a specific bundle regardless of environment variables, point `ca_bundle` at a file:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
ca_bundle = "~/certs/corp-ca-bundle.pem"
```

To disable CA forwarding for a workspace:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
ca_bundle = false
```

To apply the same bundle to every workspace, set it under `[defaults]`; a workspace can still override it:

```toml
[defaults]
ca_bundle = "~/certs/corp-ca-bundle.pem"

[workspaces.public-only]
paths = ["/home/you/projects/public-only"]
ca_bundle = false
```

!!! note
    Automatic discovery fails `jailoc up` or a running-workspace `jailoc add` restart if the selected environment variable (`SSL_CERT_FILE`, or `NIX_SSL_CERT_FILE` when `SSL_CERT_FILE` is unset or blank) points at an invalid PEM bundle — it does not silently fall through to the next variable. If neither variable is set, no bundle is forwarded and no error occurs. Setting `enable_docker = false` disables only the DinD consumer; the opencode container still receives and validates the bundle.

!!! warning
    The bundle is trusted by tools in the opencode container and by the OpenCode Node.js process. When DinD is enabled, the daemon trusts it too. Trust does not extend to the host, inner containers started with `docker run`, or `RUN` steps executed during a Dockerfile build inside DinD. `SSL_CERT_DIR` is not read as a source. This is unrelated to the `/certs/ca` mutual-TLS material used for opencode-to-dind daemon authentication. Existing `allowed_hosts`/`allowed_networks` firewall rules still apply — forwarding a CA bundle does not open network access to a private endpoint.
