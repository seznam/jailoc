# Network Isolation

A workspace VM has one network interface, and the only thing on the other end is the workspace's network broker on the host. The broker runs a userspace TCP/IP stack (gVisor). It terminates every guest connection itself and opens a real host socket only after the destination passes the workspace policy. Nothing in the guest can change that policy, not even root, because it isn't enforced in the guest.

When the broker isn't running the guest has no network at all, and the supervisor stops the VM if the broker exits.

## The virtual network

| | IPv4 | IPv6 |
|---|---|---|
| Guest address | `192.168.127.2/24` | `fd00:127::2/64` |
| Gateway (broker) | `192.168.127.1` | `fd00:127::1`, default route via `fe80::1` |

The gateway serves DHCP and DNS. Everything else the guest sends is a new flow the broker has to decide on.

## The policy

Every new TCP connection and UDP flow gets one decision. The broker checks, in order:

| Destination | Decision |
|-------------|----------|
| The gateway, the guest's own subnet, any address of a host interface | denied |
| Hard-denied ranges (below) | denied, no allow list opens them |
| Port 53 anywhere, when filtered DNS is on | denied |
| Private and reserved ranges | denied unless covered by `allowed_networks` or a resolved `allowed_hosts` address |
| Everything else (public addresses) | allowed |

### IPv4

Private and reserved IPv4 ranges: `0.0.0.0/8`, `10.0.0.0/8`, `100.64.0.0/10` (CGNAT), `127.0.0.0/8`, `169.254.0.0/16`, `172.16.0.0/12`, `192.0.0.0/24`, `192.0.2.0/24`, `192.88.99.0/24`, `192.168.0.0/16`, `198.18.0.0/15`, `198.51.100.0/24`, `203.0.113.0/24`, `224.0.0.0/4`, `240.0.0.0/4`.

Hard-denied: `0.0.0.0/8`, loopback `127.0.0.0/8`, multicast `224.0.0.0/4`, `240.0.0.0/4`, and the cloud metadata endpoints `169.254.169.254` and `169.254.170.2`.

### IPv6

Hard-denied regardless of allow lists: unspecified `::`, loopback `::1`, IPv4-compatible `::/96`, link-local `fe80::/10`, site-local `fec0::/10`, multicast `ff00::/8`, documentation `2001:db8::/32` and `3fff::/20`, discard `100::/64`, ORCHID `2001:10::/28` and `2001:20::/28`, benchmarking `2001:2::/48`, Teredo `2001::/32`, local-use NAT64 `64:ff9b:1::/48`, and the cloud metadata addresses `fd00:ec2::254` and `fd00:ec2::23`.

IPv6 addresses that carry an IPv4 address (IPv4-mapped `::ffff:0:0/96`, NAT64 `64:ff9b::/96`, 6to4 `2002::/16`) are judged by the IPv4 rules for the embedded address, so they can't be used to reach a blocked IPv4 range.

Unique local addresses (`fc00::/7`) count as private: they're reachable only through `allowed_networks` or `allowed_hosts`. Anything outside global unicast `2000::/3` is denied. The rest of `2000::/3` is public and allowed.

## Allow lists

`allowed_networks` takes IPv4 and IPv6 CIDRs. `allowed_hosts` names are resolved on the host when the VM starts and on `jailoc reload`; each A record becomes a `/32` and each AAAA record a `/128`. A name that doesn't resolve produces a warning and is skipped. Allow lists open private ranges only; they can't open hard-denied ones.

DNS answers never change a decision on their own. A public name that resolves to a private address is still blocked unless that address is allowed.

## Filtered DNS

With `filtered_dns`, the broker's resolver forwards queries to the configured upstream and answers NXDOMAIN for names in `dns_blocked_zones` and their subdomains, for both A and AAAA queries. DNS to port 53 on any other address is denied, so the guest can't bypass the filter by choosing its own resolver.

## Enforcement details

Denied TCP connections are reset and denied UDP gets ICMP port unreachable, so tools fail fast instead of hanging. Each decision is written to `broker.jsonl` in the workspace's VM state directory.

An invalid policy at start keeps the VM from starting. If a policy reload fails, the broker switches to a deny-all policy rather than keep the old one.

Containers started by the guest's own Docker daemon (`enable_docker`) send their traffic through the same interface, so the same policy applies to them.

See [Configure network access](../how-to/network-access.md) for setup and [Threat Model](threat-model.md) for why the policy looks like this.
