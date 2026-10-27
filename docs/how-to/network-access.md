# How to allow specific hosts or networks

All guest traffic leaves through jailoc's network broker on the host. Public destinations are reachable; private and reserved addresses are blocked unless you allow them. This guide shows how to open specific hostnames or CIDR ranges. For the model behind it, see [Network isolation](../explanation/network-isolation.md).

## Allow a hostname

Add it to `allowed_hosts`:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = [
  "internal-registry.example.com",
  "internal-mcp.example.com",
]
```

jailoc resolves each name on the host when the VM starts or reloads, and allows every A and AAAA address it gets (as a `/32` or `/128`) on all ports.

!!! note
    Addresses aren't re-resolved while the VM runs. If a host's address changes, run `jailoc reload <workspace>`.

## See what the agent connects to

```bash
jailoc traffic -f
```

Each line shows a connection with the name it was looked up by and the verdict; denied ones show the reason (for example `denied-range`), which tells you which host or range to allow. See [`jailoc traffic`](../reference/cli.md#jailoc-traffic).

## Allow a CIDR range

Use `allowed_networks` to open a whole subnet on all ports. IPv4 and IPv6 ranges are both accepted:

```toml
[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = [
  "172.20.0.0/16",
  "10.10.5.0/24",
  "fd12:3456:789a::/48",
]
```

Invalid CIDRs are rejected at config load time.

## Reach private IPv6 services

The guest has IPv6 (`fd00:127::2/64`). Global IPv6 addresses are reachable like public IPv4 ones. Unique local addresses (`fc00::/7`) count as private:

- a ULA service is reachable only when its range is in `allowed_networks`;
- a name in `allowed_hosts` that resolves to a ULA address opens that single address.

!!! warning
    Some ranges stay blocked whatever you allow: loopback, link-local, multicast, documentation and other reserved IPv6 ranges, Teredo, cloud metadata endpoints, host addresses and the broker gateway. IPv4 embedded in IPv6 (IPv4-mapped, NAT64 `64:ff9b::/96`, 6to4) follows the IPv4 rules for the embedded address.

## Apply rules to all workspaces

Put the lists in `[defaults]`:

```toml
[defaults]
allowed_hosts = ["internal-registry.example.com"]
allowed_networks = ["10.0.0.0/8"]
```

Workspace lists are merged with the defaults, not replacing them. Duplicates are removed.

## Apply changes to a running workspace

```bash
jailoc reload api
```

New connections follow the new rules, and `allowed_hosts` names are resolved again. Running sessions keep going.

## Allow an HTTP proxy on a private network

If your traffic goes through a proxy on a private address, allow the proxy and pass its URL to sessions:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
allowed_hosts = ["proxy.internal.example.com"]
env = [
  "HTTP_PROXY=http://proxy.internal.example.com:3128",
  "HTTPS_PROXY=http://proxy.internal.example.com:3128",
]
```

Without the allow entry, every proxied request fails.

!!! note
    Image builds get the host's proxy variables as build arguments automatically. Only sessions need the `env` entries.

## Allow local-network hosts on macOS

On macOS, reaching hosts on your local network needs the macOS Local Network permission for the process that runs jailoc (usually your terminal). Without it, connections to those addresses fail with `no route to host` even when they're allowed.

## Restrict internal DNS names

Send guest DNS through a filtered resolver and reject chosen zones:

```toml
[workspaces.api]
paths = ["/home/you/projects/api"]
filtered_dns = true
dns_upstream = "10.20.30.53"
dns_blocked_zones = ["corp.example.com", "internal"]
```

Run `jailoc reload api` to apply it to a running workspace. Names in the blocked zones and their subdomains get NXDOMAIN, for both A and AAAA queries. Other names resolve through `dns_upstream`. DNS (port 53) to any other resolver is blocked. Set the fields under `[defaults]` to cover several workspaces; a workspace can override them, including `filtered_dns = false`.

Don't put a blocked name or one of its subdomains in `allowed_hosts`. jailoc rejects that combination, including when either side comes from `[defaults]`.

!!! warning
    This filters plain DNS only. DNS over HTTPS, proxies and literal IP addresses bypass it. The shared builder VM doesn't filter DNS; use `isolated_build = true` to apply the filter to builds.

See the [configuration reference](../reference/configuration.md) for field constraints.

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
    A CA bundle doesn't open network access. Private endpoints still need `allowed_hosts` or `allowed_networks`.
