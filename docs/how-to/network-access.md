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
