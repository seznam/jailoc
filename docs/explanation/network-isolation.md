# Network Isolation

jailoc's network model is built around a simple premise: a coding agent needs the public internet to do its job (fetching packages, cloning repos, calling APIs), but it should never be able to reach your internal infrastructure. The isolation isn't about locking the agent in a box with no network. It's about making a specific cut between "things an agent legitimately needs" and "things it shouldn't touch".

For the security rationale behind why network controls matter for AI agents specifically, see [Threat Model](threat-model.md).

## What gets blocked and why

The iptables rules installed during container startup drop traffic to three address ranges:

| Range | Description | Why blocked |
|-------|-------------|------------|
| `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` | RFC 1918 private addresses | Covers most home routers, office LANs, and cloud VPC internals |
| `169.254.0.0/16` | Link-local (including AWS/GCP metadata endpoints) | Cloud metadata services expose credentials and IAM roles |
| `100.64.0.0/10` | CGNAT / Tailscale / shared address space | Commonly used by VPNs and overlay networks |

These ranges are blocked because they're where internal services live. Your Kubernetes cluster, your database, your CI runner, your cloud provider's metadata API that hands out instance credentials — they're all in RFC 1918 or adjacent space. Blocking these ranges at the iptables level means the agent can't reach them even if it tries, regardless of what it discovers about the host's network configuration.

Public internet traffic is not blocked. The DROP rules are appended after the ACCEPT rules, so anything not matching the private ranges goes through normally. The agent can `go get`, `npm install`, `pip install`, clone from GitHub, call OpenAI's API, and reach any public MCP server without any special configuration.

## How the allowlist works

The rule ordering is the key mechanism. When the entrypoint installs iptables rules, it inserts ACCEPT rules at the top of the chain before adding the DROP rules at the bottom. Rules are evaluated in order, so an ACCEPT for a specific host wins against a later DROP for its containing range.

Three things always get ACCEPT rules regardless of your config:

1. The dind container's address (so the agent can reach its Docker daemon)
2. The host gateway (so DNS and the Docker bridge work)
3. Any hosts or networks you've explicitly allowed in your workspace config

DNS resolver addresses from `/etc/resolv.conf` also get ACCEPT rules, but only for port 53 (UDP and TCP). This is necessary because some container runtimes (notably Podman) place the DNS resolver at an address inside a blocked private range. Without this rule, hostname resolution would fail and the allowed-hosts mechanism would be useless. The rule is scoped to port 53 so the resolver IP cannot be used as a general-purpose gateway into the private network.

A final ACCEPT rule allows TCP replies on the published service port (4096) for connections that were initiated from outside the container. This exists because port-forwarded traffic from the host arrives with a source address in a private range — without this rule, the container's reply packets would hit the DROP rules and the connection would hang. The rule is scoped to established TCP connections on the service port only, so it does not open any new outbound paths.

If you need the agent to reach an internal service, adding it to `allowed_hosts` or `allowed_networks` in your workspace config causes an ACCEPT rule to be inserted before the DROP rules fire. The agent can then reach that specific address while everything else in the private range stays blocked.

For step-by-step instructions, see [How-to: Network Access](../how-to/network-access.md).

## The dind sidecar is a special case

The dind container shares the same Docker network as the opencode container. iptables rules in the opencode container control egress from that container — they don't apply to the dind container at all. The dind daemon listens on port 2376 with mutual TLS, so only the opencode container (which holds the client certificates) can connect to it.

Any containers the agent starts through dind inherit the dind daemon's network configuration, not the opencode container's iptables rules. If an agent-started container needs to reach a specific address, that's determined by dind's setup, not by jailoc's iptables. This is a deliberate tradeoff: the agent's direct network is controlled, but containers-within-containers operate outside that control layer.

## What is isolated

- The agent runs as an unprivileged user (UID 1000) with all Linux capabilities dropped and `no_new_privs` set
- Resource limits apply: 4 GB RAM, 2 CPUs, 256 PIDs
- OpenCode configuration directories are mounted read-only — the agent can read your settings but cannot modify them on the host
- The agent's data volume (SQLite history, auth tokens) is a named Docker volume, completely separate from your host's `~/.local/share/opencode`
- The agent gets its own Docker daemon through dind — it never touches the host Docker socket, so containers it starts cannot escape to the host
- Network egress to private and internal ranges is blocked by iptables

## What is not isolated

- The dind container itself runs `--privileged`. This is unavoidable for nested Docker support.
- The public internet is fully open from the opencode container
- API keys present in your mounted `opencode.json` are readable inside the container (the agent needs them to function)
- No seccomp profile or AppArmor profile is applied beyond Docker's defaults
- The root filesystem is not read-only

These gaps are intentional or accepted tradeoffs, not oversights. The goal of jailoc is to protect your internal network and keep the agent's state from bleeding into your host environment — not to prevent the agent from doing its job on the public internet.
