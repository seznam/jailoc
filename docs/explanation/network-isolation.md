# Network Isolation

A workspace VM has one network interface, and the only thing on the other end is the workspace's network broker on the host. The broker runs a userspace TCP/IP stack (gVisor). It terminates every guest connection itself and opens a real host socket only after the destination passes the workspace policy. Nothing in the guest can change that policy, not even root, because it isn't enforced in the guest.

When the broker isn't running the guest has no network at all, and the supervisor stops the VM if the broker exits.

## The virtual network

| | IPv4 | IPv6 |
|---|---|---|
| Guest address | `192.168.127.2/24` | `fd00:127::2/64` |
| Gateway (broker) | `192.168.127.1` | `fd00:127::1`, default route via `fe80::1` |

IPv6 is opt-in with `network.ipv6`. Without it the guest disables IPv6 on its interface and the broker denies every IPv6 destination, so the guest is IPv4 only. The broker check doesn't depend on the guest: guest root re-enabling IPv6 still gets denied.

The gateway serves DHCP, DNS, SNTP with the host clock (UDP port 123, used by `jailoc-guest` to keep the guest clock on host time) and the host services configured with `host_ports`. Everything else the guest sends is a new flow the broker has to decide on.

## The policy

Every new TCP connection and UDP flow gets one decision. The broker evaluates rules in order:

| Step | Decision |
|------|----------|
| The gateway, the guest's own subnet, any address of a host interface | denied |
| Any IPv6 address, when `network.ipv6` is off | denied |
| Hard-denied ranges (below) | denied, no rule opens them |
| Direct DNS (port 53) to any address other than the gateway | denied |
| Any matching `deny` rule | denied (`rule=<name>`) |
| Any matching `allow` rule (including the implicit `agent` rule) | allowed (`rule=<name>`) |
| Everything else | denied (`default-deny` or `denied-range`) |

![The network broker checks each new flow from the guest in order: its own addresses, hard-denied ranges, port 53 when filtered DNS is on, and private ranges unless allow-listed are denied with a reset or ICMP; public addresses are allowed through a real host socket. DNS queries to the gateway go through the filtered resolver, which answers NXDOMAIN for blocked zones and for answers naming them.](img/network-policy.svg)

### IPv4

Private and reserved IPv4 ranges: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10` (CGNAT), `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`.

Hard-denied: `0.0.0.0/8`, loopback `127.0.0.0/8`, multicast `224.0.0.0/4`, `240.0.0.0/4`, and the cloud metadata endpoints `169.254.169.254` and `169.254.170.2`.

### IPv6

These rules apply when `network.ipv6` is on.

Hard-denied regardless of allow rules: unspecified `::`, loopback `::1`, IPv4-compatible `::/96`, link-local `fe80::/10`, site-local `fec0::/10`, multicast `ff00::/8`, documentation `2001:db8::/32` and `3fff::/20`, discard `100::/64`, ORCHID `2001:10::/28` and `2001:20::/28`, benchmarking `2001:2::/48`, Teredo `2001::/32`, local-use NAT64 `64:ff9b:1::/48`, and the cloud metadata addresses `fd00:ec2::254` and `fd00:ec2::23`.

IPv6 addresses that carry an IPv4 address (IPv4-mapped `::ffff:0:0/96`, NAT64 `64:ff9b::/96`, 6to4 `2002::/16`) are judged by the IPv4 rules for the embedded address, so they can't be used to reach a blocked IPv4 range.

Unique local addresses (`fc00::/7`) count as private: they're reachable only through an explicit IPv6 CIDR rule. Anything outside global unicast `2000::/3` is denied.

## Enforcement by name

Rules from `[defaults.network.rules]` and `[workspaces.<name>.network.rules]`, together with the implicit `agent` rule for the workspace's coding agent, are combined into a single set where `deny` always wins over `allow`. `@list` presets (such as `@dev`) are expanded on the host before the policy reaches the broker.

The broker enforces hostname rules at three points:

1. **DNS**: the gateway resolver checks the queried name and every record owner and CNAME target in the upstream answer against the policy. Names that match a `deny` rule or do not match any `allow` rule get NXDOMAIN. `HTTPS` and `SVCB` records are answered with NODATA so Encrypted ClientHello (ECH) configurations do not reach the guest. Allowed `A` and `AAAA` answers populate a per-workspace IP-to-name cache bounded by record TTL. Direct port 53 to any other resolver is denied, and DoT (port 853) and QUIC (UDP port 443) are denied unless a rule explicitly opens those ports.
2. **Connect time (L4)**: when the guest opens a TCP connection or UDP flow, the broker looks up the destination IP in its DNS cache. A rule matches if the IP is within a rule's CIDR (or `*` for public addresses), or if a cached DNS name for that IP matches a rule's hostname or `*.wildcard` pattern on the matching protocol and port. Unresolved literal IPs only match CIDR or `*` rules, and hostname rules never open private IP ranges.
3. **TLS SNI and HTTP Host before dialing**: for TCP connections authorized by a hostname rule or on ports with hostname deny rules, the broker completes the guest TCP handshake, reads the TLS ClientHello SNI or HTTP/1 `Host` header, re-evaluates the policy against that name, verifies the name belongs to the destination IP in the DNS cache, and only then dials the upstream host socket. A denied or mismatched SNI resets the connection before any upstream socket is opened, closing the shared-CDN-IP hole.

## Enforcement details

Denied TCP connections are reset and denied UDP gets ICMP port unreachable, so tools fail fast instead of hanging. Each decision is written to `broker.jsonl` in the workspace's VM state directory with the resolved `name` and matching `rule`, which `jailoc traffic` prints. The broker moves the log to `broker.jsonl.1` when it reaches 16 MiB, so it keeps at most about 32 MiB. It also caps a workspace at 4096 open TCP connections, 1024 UDP flows and 128 DNS queries in flight; connections beyond the cap are refused and logged.

An invalid policy at start keeps the VM from starting. If a policy reload fails, the broker switches to a deny-all policy rather than keep the old one.

Containers started by the guest's own Docker daemon (`enable_docker`) send their traffic through the same interface, so the same policy applies to them.

See [Configure network access](../how-to/network-access.md) for setup and [Threat Model](threat-model.md) for why the policy looks like this.
