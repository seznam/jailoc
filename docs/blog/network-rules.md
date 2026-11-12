# Taming agent egress: default-deny network rules in jailoc 2.4

*November 2026 · release 2.4*

When you give a coding agent a shell, you also give it `curl`, `git`, `ssh` and every runtime's HTTP client. In jailoc 2.0, the host-side network broker blocked private IP ranges by default and left the public internet open. That stopped an agent from reaching your internal databases and Kubernetes clusters, but it left the third leg of the [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/) wide open: a prompt injection could still send workspace files or tokens to any public server on the internet.

jailoc 2.4 replaces `allowed_hosts`, `allowed_networks` and `dns_blocked_zones` with unified `[[<scope>.network.rules]]` tables, switches the broker to **default-deny egress**, and enforces hostname rules across DNS, TCP/UDP connection setup, and TLS SNI / HTTP `Host` inspection. This post explains how the rules work and walks through four practical setups.

**TL;DR**

- Egress is **default-deny**: only destinations matched by an `allow` rule (plus the selected coding agent's API endpoints) are reachable.
- `deny` rules always win over `allow` rules, regardless of order or whether the rule lives in `[defaults]` or the workspace.
- Hostname rules (`allow = ["github.com", "*.internal.example.com"]`) are enforced at three layers: gateway DNS, L4 IP-to-name cache, and TLS ClientHello SNI / HTTP `Host` before the broker dials upstream.
- Wildcard `allow = ["*"]` opens the public internet while keeping all private, CGNAT, and IPv6 ULA ranges blocked.
- Upgrading from an older config? `jailoc migrate` converts `allowed_hosts`, `allowed_networks` and `dns_blocked_zones` into `[[*.network.rules]]`.

## How a rule looks

Rules live in `[[defaults.network.rules]]` (shared across all workspaces) and `[[workspaces.<name>.network.rules]]` (added for one workspace). Each rule has a `name`, either an `allow` or `deny` target list, an optional `proto` (`"tcp"` by default, `"udp"`, or `"any"`), and optional `ports`:

```toml
[[defaults.network.rules]]
name = "dev-deps"
allow = [
  "github.com",
  "api.github.com",
  "*.githubusercontent.com",
  "registry.npmjs.org",
  "pypi.org",
  "files.pythonhosted.org",
  "proxy.golang.org",
  "sum.golang.org",
]

[[defaults.network.rules]]
name = "no-paste"
deny = ["pastebin.com", "gist.github.com", "*.ngrok.io"]

[[workspaces.api.network.rules]]
name = "staging-db"
allow = ["10.20.5.10"]
proto = "tcp"
ports = [5432]
```

Targets in `allow` and `deny` can be:

| Target | Example | What it matches |
|---|---|---|
| Exact hostname | `"github.com"` | That DNS name (public or internal IPs resolved via the gateway DNS) |
| Subdomain wildcard | `"*.githubusercontent.com"` | Any subdomain (`a.githubusercontent.com`, `a.b.githubusercontent.com`), not the apex |
| IP or CIDR | `"10.20.5.10"`, `"10.20.0.0/16"`, `"fd12:3456::/48"` | Direct IP connections and resolved hosts in that range |
| Public wildcard | `"*"` | Any **public** IPv4/IPv6 address (never private, CGNAT, or ULA ranges) |

## Implicit rules and built-in broker behavior

On top of the rules you write in `config.toml`, jailoc applies a small set of implicit rules so the VM and its coding agent work out of the box without weakening inspection:

1. **`rule=agent` (TCP port `443`)**: appended automatically for the workspace's coding agent (or the `from` harness of a custom agent) so it can authenticate and call its model APIs even with an empty `network.rules` list. A `deny` rule still overrides `rule=agent` if you want to block a specific provider:

| `agent` | Implicit `rule=agent` hostnames (`tcp/443`) |
|---|---|
| `opencode`, `opencode1`, `opencode2`, `omo` | `api.anthropic.com`, `api.openai.com`, `auth.openai.com`, `chatgpt.com`, `generativelanguage.googleapis.com`, `oauth2.googleapis.com`, `api.githubcopilot.com`, `*.githubcopilot.com`, `openrouter.ai`, `opencode.ai`, `*.opencode.ai`, `models.dev` |
| `claude` | `api.anthropic.com`, `claude.ai`, `console.anthropic.com`, `platform.claude.com` |
| `copilot` | `api.github.com`, `github.com`, `api.githubcopilot.com`, `*.githubcopilot.com`, `copilot-proxy.githubusercontent.com` |
| `agy` | `generativelanguage.googleapis.com`, `cloudcode-pa.googleapis.com`, `oauth2.googleapis.com`, `accounts.google.com` |

2. **Gateway services (`192.168.127.1`)**: the broker itself serves DHCP, DNS (TCP/UDP `53`), SNTP clock synchronization (UDP `123`), SSH/GPG agent relays (`ssh_auth_sock`, `gpg_agent`), and any configured `host_ports` on the gateway. All other traffic to the gateway, guest subnet (`192.168.127.0/24`, `fd00:127::/64`), host interface IPs, and hard-denied ranges (loopback, link-local, multicast, cloud metadata `169.254.169.254` / `fd00:ec2::254`) is unconditionally denied.
3. **DNS and SNI bypass prevention**:
    - Direct DNS (port `53`) to any IP other than the gateway is denied (`reason=dns-filtered`).
    - DNS `HTTPS` and `SVCB` records are answered with `NODATA` so Encrypted ClientHello (ECH) cannot hide the TLS SNI from the broker.
    - A wildcard rule (`allow = ["*"]`) without explicit `ports` automatically excludes TCP port `853` (DNS over TLS) and UDP port `443` (QUIC/HTTP/3), forcing HTTP clients to fall back to TCP port `443` where TLS SNI is inspected.
4. **`dev-deps` in newly created configs**: when jailoc creates `~/.config/jailoc/config.toml` for the first time, it writes an explicit `[[defaults.network.rules]]` block named `dev-deps` into the file (covering GitHub, GitLab.com, npm, Yarn, Node.js, Bun, PyPI, Astral, Go proxy, crates.io, Rustup, Docker Hub/GHCR/Quay/GCR/MCR, Ubuntu/Debian/Alpine mirrors, and Nix caches). Because it lives right in `config.toml`, you can edit or delete it like any other rule.
5. **`rule=legacy`**: if your config still has deprecated `allowed_hosts`, `allowed_networks`, or `dns_blocked_zones`, jailoc translates them in memory into `rule=legacy` entries until you run `jailoc migrate`.

## How the broker enforces hostname rules

A hostname allowlist that only resolves names at VM boot breaks on CDNs that rotate IPs every minute, and a firewall that only checks destination IPs lets a guest connect to an allowed Cloudflare IP while sending `Host: attacker.example.com` in the request.

Because `jailoc-netbroker` terminates every guest connection in userspace on the host before opening a real socket, it checks hostname rules at three points:

1. **Gateway DNS**: the guest's only DNS server is the broker gateway (`192.168.127.1`). Direct traffic to port 53 of any other IP is denied, and a wildcard `allow = ["*"]` rule excludes DoT (TCP 853) and QUIC (UDP 443) unless `ports` explicitly lists them. When the guest queries a domain, the broker checks the question and every record owner and CNAME target in the upstream response. Denied or unallowed names return `NXDOMAIN`; `HTTPS` and `SVCB` records return `NODATA` so Encrypted ClientHello (ECH) is disabled. Allowed `A` and `AAAA` answers are recorded in a per-workspace IP-to-name cache bounded by the DNS TTL (including the minimum TTL across any CNAME chain).
2. **Connect time (L4)**: when the guest opens a TCP connection or UDP flow, the broker checks hard-denied ranges (loopback, link-local, cloud metadata `169.254.169.254`, host interface addresses), then evaluates `deny` rules, then `allow` rules. A hostname rule matches only if the destination IP is currently mapped to that hostname in the workspace's DNS cache. Hard-coded IPs that were never resolved through DNS only match IP/CIDR or `"*"` rules.
3. **TLS SNI and HTTP `Host` before dialing**: when a TCP connection is authorized by a hostname rule (or when a `deny` rule has hostname patterns on that port), the broker completes the TCP handshake with the guest in memory, reads the TLS ClientHello SNI or HTTP/1 `Host` header, checks that the name is allowed by the policy **and** was resolved to that destination IP in the DNS cache, and only then dials the upstream server. For plain HTTP connections, the broker keeps inspecting pipelined or keep-alive requests on the stream so a second request cannot switch to a denied `Host`.

 Every decision is logged with the rule name that matched, so `jailoc traffic -f` tells you immediately why a connection was allowed or blocked:

```bash
jailoc traffic -f
```

## Four common setups

### 1. Strict default-deny: registries and documentation only

New `config.toml` files generated by jailoc 2.4 ship with a `dev-deps` rule in `[defaults.network.rules]` that allows GitHub, GitLab.com, npm, PyPI, Go proxy, crates.io, common container registries and Linux/Nix package mirrors. Everything else on the public internet and on your private networks is denied.

If your agent also uses documentation MCP servers or a specific API, add a small rule in `[defaults]` or the workspace:

```toml
[[defaults.network.rules]]
name = "mcp-docs"
allow = ["context7.com", "*.context7.com"]
```

If the agent tries to curl an unknown domain or a prompt injection tries to exfiltrate data to an unfamiliar host, DNS returns `NXDOMAIN` and `jailoc traffic` logs `blocked (default-deny)`.

### 2. Open public internet, internal network walled off

If your workflow pulls from arbitrary documentation sites, blogs, or third-party APIs and you want the classic jailoc 2.0 posture—all public internet allowed, all internal/private networks blocked—add `allow = ["*"]`:

```toml
[[defaults.network.rules]]
name = "internet"
allow = ["*"]
proto = "any"
```

Because `"*"` only matches public IPv4 and global unicast IPv6 addresses, RFC 1918 private networks (`10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`), CGNAT (`100.64.0.0/10`), and IPv6 ULA (`fc00::/7`) remain completely blocked.

You can still combine `allow = ["*"]` with `deny` rules to block known exfiltration or paste services across all workspaces:

```toml
[[defaults.network.rules]]
name = "no-exfil"
deny = [
  "pastebin.com",
  "gist.github.com",
  "*.ngrok.io",
  "*.ngrok-free.app",
  "webhook.site",
]
proto = "any"
```

Because `deny` always wins over `allow`, those domains are blocked both at DNS and at TLS SNI / HTTP `Host` inspection even when `allow = ["*"]` is active.

### 3. Corporate VPN / internal services (Seznam, GitLab, internal mirrors)

In a corporate environment, your workspaces often need internal GitLab, private package mirrors (PyPI, npm, Harbor/Docker), internal Kubernetes APIs, or staging services that resolve to private `10.x` or `172.16.x` addresses.

With hostname rules, you don't need to look up or maintain internal IP ranges. Allow the internal hostnames or wildcard zones in the workspace, and pair them with your internal CA bundle:

```toml
[workspaces.work]
paths = ["~/work"]
ca_bundle = "~/work/corp-root-ca.pem"

[[workspaces.work.network.rules]]
name = "corp"
allow = [
  "gitlab.corp.example.com",
  "*.registry.corp.example.com",
  "*.dev.k8s.corp.example.com",
]
proto = "any"
```

What happens when the agent connects to `gitlab.corp.example.com` (say `10.20.30.40`):

1. The guest queries `gitlab.corp.example.com` via the gateway DNS. The `corp` rule matches the name, so the broker forwards the query to `[<scope>.network] dns` (or the nameservers in `/etc/resolv.conf` when `dns` is unset; on macOS with scoped VPN resolvers that do not appear in `/etc/resolv.conf`, set `dns` to your internal VPN resolver IP) and caches `10.20.30.40 -> gitlab.corp.example.com` for the record's TTL.
2. When the guest connects to `10.20.30.40:443` (HTTPS) or `10.20.30.40:22` (SSH), the broker sees that `10.20.30.40` was resolved from `gitlab.corp.example.com`, which matches `rule=corp`.
3. Every other host on `10.0.0.0/8` that wasn't resolved through an allowed hostname remains blocked as `denied-range`.

If a tool connects to a raw internal IP without DNS (for example a database in a local test environment or an HTTP proxy), allow that IP or subnet on just the port it needs:

```toml
[[workspaces.work.network.rules]]
name = "dev-postgres"
allow = ["10.20.5.10"]
proto = "tcp"
ports = [5432]
```

### 4. Blocking sensitive internal zones while allowing a specific host

Suppose a workspace has broad internal access or you want to guarantee that production subdomains under `*.prod.corp.example.com` can never be reached, even if a broader `*.corp.example.com` allow rule is present:

```toml
[[workspaces.work.network.rules]]
name = "corp-dev"
allow = ["*.corp.example.com"]
proto = "any"

[[workspaces.work.network.rules]]
name = "no-prod"
deny = ["prod.corp.example.com", "*.prod.corp.example.com"]
proto = "any"
```

`deny` rules are evaluated before `allow` rules at DNS, L4, and L7—including across CNAME targets in DNS answers—so even if a dev hostname CNAMEs to a `*.prod.corp.example.com` target, the broker answers `NXDOMAIN`.

## Migrating an existing `config.toml`

If your configuration still uses `allowed_hosts`, `allowed_networks`, or `[*.network]` `dns_upstream` and `dns_blocked_zones`, jailoc 2.4 continues to honor them in memory and prints a deprecation warning (`[*.network] filtered_dns` is ignored with a warning because DNS filtering is always on, and top-level `filtered_dns`, `dns_upstream`, and `dns_blocked_zones` outside `[*.network]` require migration). Run `jailoc migrate` to rewrite `config.toml` automatically:

```bash
jailoc migrate --dry-run    # preview the rewritten config.toml
jailoc migrate -y           # apply and save a backup
```

`jailoc migrate` turns:

- `allowed_hosts` into a `name = "allowed-hosts"` allow rule (`proto = "any"`),
- `allowed_networks` into a `name = "allowed-networks"` allow rule (`proto = "any"`),
- `dns_blocked_zones` into a `name = "blocked-zones"` deny rule covering `<zone>` and `*.<zone>`,
- `dns_upstream` into `network.dns`,
- and adds `allow = ["*"]` if your config had no `[[*.network.rules]]` yet, so existing workspaces keep their public internet access until you tighten the rules.

After editing rules by hand, apply them to a running workspace without restarting the VM or dropping your agent sessions:

```bash
jailoc reload
```

Read more: [Configure network access](../how-to/network-access.md), [Network isolation](../explanation/network-isolation.md), [Configuration reference](../reference/configuration.md#defaultsnetwork).
