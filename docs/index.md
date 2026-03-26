![jailoc](hero.jpeg)

# jailoc

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network restrictions and privilege dropping, letting you control exactly which directories and internal services the agent can reach.

## Why jailoc

AI coding agents are powerful — and dangerous when unsandboxed. An agent running directly on your host inherits your user account: every file, every credential, every network endpoint you can reach. A single [prompt injection](explanation/threat-model.md) hidden in a GitHub issue, a web page, or a package README can instruct the agent to read sensitive data and send it somewhere it shouldn't go. This is the [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/): private data access + untrusted content + external communication = data theft.

jailoc cannot break the trifecta entirely — the agent still processes untrusted content from the public internet, because that's its job. What jailoc does is **shrink the blast radius** by tightening the other two legs:

**Your files stay out of reach.** The agent only sees directories you explicitly mount as workspaces. Your SSH keys, browser profiles, other projects, and anything else on the host filesystem are invisible inside the container. The agent runs as an unprivileged user (UID 1000) with all Linux capabilities dropped and `no_new_privs` set.

**Your internal network is walled off.** Agents fetch packages and call APIs over the public internet, but without isolation they can also reach your Kubernetes clusters, databases, and cloud metadata endpoints. jailoc blocks all private networks (RFC 1918, link-local, CGNAT) by default via iptables — you allowlist only what the agent actually needs. A compromised agent cannot pivot to internal infrastructure.

**The Docker socket stays untouched.** Agents often need Docker for building and testing, but mounting `/var/run/docker.sock` lets them escape the sandbox by starting privileged containers on your host. jailoc gives each workspace its own isolated Docker daemon via a DinD sidecar — agent-started containers stay inside the sandbox.

!!! warning "What jailoc does not protect against"

    The agent can still make outbound requests to the **public internet**. If a prompt injection attack instructs the agent to exfiltrate workspace source code to an attacker-controlled public server, jailoc's network rules will not block it. jailoc protects your internal infrastructure and limits file access — it is not a complete defense against the [lethal trifecta](explanation/threat-model.md).

## Documentation

### Get started

New to jailoc? Start here and run your first workspace in minutes.

- [Getting Started](tutorials/getting-started.md) — install jailoc and run your first workspace

### How-to guides

Step-by-step guides for specific tasks once you're up and running.

- [Installation](how-to/installation.md)
- [Workspace Configuration](how-to/workspace-configuration.md)
- [Custom Images](how-to/custom-images.md)
- [Network Access](how-to/network-access.md)
- [Access Modes](how-to/access-modes.md)

### Reference

Complete technical descriptions of every CLI command and configuration field.

- [CLI Reference](reference/cli.md)
- [Configuration Reference](reference/configuration.md)
- [Image Resolution](reference/image-resolution.md)
- [Overlay Compatibility](reference/overlay-compatibility.md)

### Explanation

Background on how jailoc works and why it's designed the way it is.

- [Overview](explanation/overview.md)
- [Threat Model](explanation/threat-model.md)
- [Container Architecture](explanation/container-architecture.md)
- [Network Isolation](explanation/network-isolation.md)
- [Access Modes](explanation/access-modes.md)

### Development

- [Contributing & Development](development.md)
