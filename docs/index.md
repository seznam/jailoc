![jailoc](hero.jpeg)

# jailoc

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network restrictions and privilege dropping, letting you control exactly which directories and internal services the agent can reach.

## Why jailoc

**Your agent can read anything you can.** Running an agent directly on your host means trusting it with every file, credential, and network endpoint your user account can reach. jailoc draws a hard boundary — the agent runs unprivileged in its own container with dropped capabilities and `no_new_privs`, only seeing directories you explicitly mount.

**Your internal network is one `curl` away.** Agents routinely fetch packages and call APIs over the public internet, but without isolation they can also reach your Kubernetes clusters, databases, and cloud metadata endpoints. jailoc blocks all private networks (RFC 1918, link-local, CGNAT) by default via iptables — you allowlist only what the agent actually needs.

**Sharing the Docker socket is a sandbox escape.** Agents often need Docker for building and testing, but mounting `/var/run/docker.sock` lets them break out by starting privileged containers on your host. jailoc gives each workspace its own isolated Docker daemon via a DinD sidecar — agent-started containers stay inside the sandbox.

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
- [Container Architecture](explanation/container-architecture.md)
- [Network Isolation](explanation/network-isolation.md)
- [Access Modes](explanation/access-modes.md)

### Development

- [Contributing & Development](development.md)
