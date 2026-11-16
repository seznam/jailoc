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
| Direct DNS (port 53) to any address other than the gateway, unless an `allow` rule lists port 53 in `ports` | denied (`dns-filtered`) |
| Any matching `deny` rule | denied (`rule=<name>`) |
| Any matching `allow` rule (including the implicit `agent` rule) | allowed (`rule=<name>`) |
| Rules with `http` matching the connection | requests decided one by one, see [TLS inspection](#tls-inspection) |
| Everything else | denied (`default-deny` or `denied-range`) |

![The network broker checks each new flow from the guest in order: its own addresses, hard-denied ranges, port 53 when filtered DNS is on, and private ranges unless allow-listed are denied with a reset or ICMP; public addresses are allowed through a real host socket. DNS queries to the gateway go through the filtered resolver, which answers NXDOMAIN for blocked zones and for answers naming them.](img/network-policy.svg)

### IPv4

Private and reserved IPv4 ranges: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10` (CGNAT), `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`.

Hard-denied: `0.0.0.0/8`, loopback `127.0.0.0/8`, multicast `224.0.0.0/4`, `240.0.0.0/4`, and the cloud metadata endpoints `169.254.169.254` and `169.254.170.2`.

### IPv6

These rules apply when `network.ipv6` is on.

Hard-denied regardless of allow rules: unspecified `::`, loopback `::1`, IPv4-compatible `::/96`, link-local `fe80::/10`, site-local `fec0::/10`, multicast `ff00::/8`, documentation `2001:db8::/32` and `3fff::/20`, discard `100::/64`, ORCHID `2001:10::/28` and `2001:20::/28`, benchmarking `2001:2::/48`, Teredo `2001::/32`, local-use NAT64 `64:ff9b:1::/48`, and the cloud metadata addresses `fd00:ec2::254` and `fd00:ec2::23`.

IPv6 addresses that carry an IPv4 address (IPv4-mapped `::ffff:0:0/96`, NAT64 `64:ff9b::/96`, 6to4 `2002::/16`) are judged by the IPv4 rules for the embedded address, so they can't be used to reach a blocked IPv4 range.

Unique local addresses (`fc00::/7`) count as private: they're reachable only through an explicit IPv6 CIDR rule or a hostname rule for a name in `internal_zones` (never `*`). Anything outside global unicast `2000::/3` is denied.

## Enforcement by name

Rules from `[defaults.network.rules]` and `[workspaces.<name>.network.rules]`, together with the implicit `agent` rule for the workspace's coding agent, are combined into a single set where `deny` always wins over `allow`.

The broker enforces hostname rules at three points:

1. **DNS**: the gateway resolver checks the queried name and every record owner and CNAME target in the upstream answer against the policy. Names that match a `deny` rule or do not match any `allow` rule get NXDOMAIN. `HTTPS` and `SVCB` records are answered with NODATA so Encrypted ClientHello (ECH) configurations do not reach the guest. `A` and `AAAA` records with a private address are dropped unless the queried name, the record owner and every `CNAME` in the chain are in `internal_zones`. The remaining `A` and `AAAA` answers populate a per-workspace IP-to-name cache bounded by record TTL. Direct port 53 to any other resolver is denied unless an `allow` rule lists port 53 in `ports`; the broker then answers those queries itself, sends them only to that resolver and filters them like the gateway resolver, but their answers stay out of the IP-to-name cache, so they never open a hostname rule. DoT (port 853) and QUIC (UDP port 443) are denied unless a rule explicitly opens those ports.
2. **Connect time (L4)**: when the guest opens a TCP connection or UDP flow, the broker looks up the destination IP in its DNS cache. A rule matches if the IP is within a rule's CIDR (or `*` for public addresses), or if a cached DNS name for that IP matches a rule's hostname or `*.wildcard` pattern on the matching protocol and port. Unresolved literal IPs only match CIDR or `*` rules, and `*` never opens private IP ranges. A hostname rule opens a private address only through a cached name in `internal_zones` (`reason=not-internal-zone` otherwise).
3. **TLS SNI and HTTP Host before dialing**: for TCP connections authorized by a hostname rule or on ports with hostname deny rules, or matched by a rule with `http`, the broker completes the guest TCP handshake, reads the TLS ClientHello SNI or HTTP/1 `Host` header, re-evaluates the policy against that name, verifies the name belongs to the destination IP in the DNS cache, and only then dials the upstream host socket. A denied or mismatched name is refused before any upstream socket is opened, closing the shared-CDN-IP hole. The broker answers in the protocol the client spoke: a TLS `access_denied` alert, or for plain HTTP a `403 Forbidden` whose body names the rule (`blocked by network policy (rule=no-paste)`), so clients report a policy denial instead of a connection reset. A later request on a kept-alive HTTP connection gets the same 403 as long as the upstream has sent nothing to the guest yet; otherwise, and for malformed requests, the connection is reset.
4. **HTTP request**: for connections covered by a rule with `http`, the broker decides every request by method and path. See [TLS inspection](#tls-inspection).

## TLS inspection

Hostname rules see the destination and the server name, not what the client asks for there. A rule with an [`http` table](../reference/configuration.md#http-rules) restricts methods and paths, so the broker has to read the requests. It does that for the flows such rules cover and no others: it terminates the guest's TLS connection itself, reads the requests, applies the policy to each and forwards the allowed ones over a second TLS connection to the real server. A connection that matches no `http` rule is spliced as before and stays opaque.

### The inspection CA

The broker presents certificates signed by a per-workspace CA (ECDSA P-256, valid for ten years), created on the host the first time a rule has `http`. The private key stays on the host in the VM state directory with mode `0600`, outside every share and file the guest receives; it never enters the VM. The key and certificate stay until `jailoc down --purge` deletes the state directory; the next `up` creates a new CA.

The CA certificate goes into the guest's CA bundle and trust stores, also with `ca_bundle = false`. Every process in the guest therefore trusts it, guest root and the agent included. That is what lets the broker read decrypted traffic for the hosts matched by `http` rules: it sees the full requests and responses, headers and bodies included. The broker writes only method, host, path (no query string) and status to `broker.jsonl`. Hosts no `http` rule covers stay encrypted end to end.

The leaf certificates the broker issues are valid for 24 hours, issued per server name and cached in memory. Whoever holds the CA key can sign certificates for any name that the guest trusts, so the state directory of the workspace deserves the same care as the other host-side files of the VM.

### Upstream verification

The broker verifies the upstream certificate against the host's system roots plus the file named by `ca_bundle` when it is a path. A server whose certificate fails verification gets no request: the client receives `502`, and `jailoc traffic` shows the error with a hint. The broker does not chain through an upstream proxy ([#398](https://github.com/seznam/jailoc/issues/398)).

### Protocol detection

The broker decides per connection whether it speaks HTTP. A ClientHello that offers `h2` or `http/1.1` in ALPN is decrypted and served as HTTP. One that offers ALPN without either is not HTTP, and plain rules decide, with no decryption. Without ALPN the broker decrypts, waits up to one second for the first line and serves HTTP/1.1 or HTTP/2 when it is a request line or the HTTP/2 preface; anything else is decided by the plain rules and, if allowed, relayed to a verified upstream connection. A stream that stays silent is relayed too (server-first protocols), but a request the guest sends later closes it. Silent or server-first protocols, which show no server name, are decided by the plain rules and never opened by an `http` rule. A connection without a name is inspected under the destination's IP address only when an `http` rule matches that address. Plain HTTP on cleartext ports, including HTTP/2 with prior knowledge (`h2c`), is checked request by request without TLS.

A client that rejects the broker's certificate, because it pins certificates, keeps its own CA store or uses mutual TLS, fails the handshake. The broker logs an `inspect-error` event with a hint, and the connection ends. Removing the `http` table from the rules matching that host passes the traffic through undecrypted.

### What inspection doesn't cover

A WebSocket upgrade request is checked like any request. After the `101` response the connection is relayed unfiltered, and the frames aren't inspected ([#397](https://github.com/seznam/jailoc/issues/397)). `CONNECT` requests are denied, so a client can't tunnel through an inspected host. Secret injection and masking of sensitive data in requests aren't part of inspection ([#93](https://github.com/seznam/jailoc/issues/93), [#94](https://github.com/seznam/jailoc/issues/94)).

## Private addresses and DNS rebinding

Whoever runs DNS for a name decides which addresses it resolves to. If any name a rule allows could resolve to a private address and open it, a rule like `allow = ["*.nip.io"]` (`10-0-0-5.nip.io` resolves to `10.0.0.5`), or an allowed name whose zone a third party controls, would open the user's LAN and internal networks. That is DNS rebinding.

So trust in private answers is set per DNS zone, not per rule. `internal_zones` lists the zones whose DNS you control, such as your company's internal domain. Only names in them may resolve to private addresses. The broker drops any other private answer before it reaches the guest or the IP-to-name cache. A `CNAME` chain that touches a public name anywhere is dropped too, because that name's owner could otherwise choose which host the internal zone publishes the guest reaches. A zone opens nothing by itself: its names still need an `allow` rule, and `deny` rules still win. See [Internal zones](../reference/configuration.md#internal-zones).

## Enforcement details

Other denied TCP connections are reset and denied UDP gets ICMP port unreachable, so tools fail fast instead of hanging. Each decision is written to `broker.jsonl` in the workspace's VM state directory with the resolved `name` and matching `rule`, which `jailoc traffic` prints. The broker moves the log to `broker.jsonl.1` when it reaches 16 MiB, so it keeps at most about 32 MiB. It also caps a workspace at 4096 open TCP connections, 1024 UDP flows, 128 UDP DNS queries in flight and 64 open DNS-over-TCP connections; the DNS caps count the gateway resolver and direct DNS to allowed resolvers together. Connections beyond a cap are refused and logged, and UDP DNS queries beyond it are dropped for the guest resolver to retry.

An invalid policy at start keeps the VM from starting. If a policy reload fails, the broker switches to a deny-all policy rather than keep the old one.

Containers started by the guest's own Docker daemon (`enable_docker`) send their traffic through the same interface, so the same policy applies to them.

See [Configure network access](../how-to/network-access.md) for setup and [Threat Model](threat-model.md) for why the policy looks like this.
