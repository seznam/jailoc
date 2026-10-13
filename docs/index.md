![jailoc](hero.jpeg)

# jailoc

`jailoc` runs coding agents (OpenCode, Claude Code, GitHub Copilot CLI, OmO) in sandboxed micro-VMs so they can work autonomously without touching your host system. Each workspace boots its own lightweight VM with network restrictions, letting you control exactly which directories and internal services the agent can reach.

## Why jailoc

AI coding agents are powerful, and dangerous when unsandboxed. An agent running directly on your host inherits your user account: every file, every credential, every network endpoint you can reach. A single [prompt injection](explanation/threat-model.md) hidden in a GitHub issue, a web page, or a package README can instruct the agent to read sensitive data and send it somewhere it shouldn't go. This is the [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/): private data access + untrusted content + external communication = data theft.

jailoc can't break the trifecta entirely. The agent still processes untrusted content from the public internet, because that's its job. What jailoc does is **shrink the blast radius** by tightening the other two legs:

**Your files stay out of reach.** Each workspace runs in its own VM with its own kernel. The agent only sees directories you explicitly share as workspace paths or mounts, plus read-only snapshots of files you list. Your SSH keys, browser profiles, other projects, and anything else on the host filesystem are invisible inside the VM.

**Your internal network is walled off.** Agents fetch packages and call APIs over the public internet, but without isolation they can also reach your Kubernetes clusters, databases, and cloud metadata endpoints. Every packet the VM sends passes through a host-side network broker that denies private and reserved IPv4 and IPv6 ranges by default. You allowlist only what the agent actually needs, so a compromised agent can't pivot to internal infrastructure.

**The Docker socket stays untouched.** Agents often need Docker for building and testing, but mounting `/var/run/docker.sock` lets them escape the sandbox by starting privileged containers on your host. With `enable_docker`, jailoc runs a Docker daemon inside the workspace VM, so agent-started containers stay inside the sandbox.

!!! warning "What jailoc does not protect against"

    The agent can still make outbound requests to the **public internet**. If a prompt injection attack instructs the agent to exfiltrate workspace source code to an attacker-controlled public server, jailoc's network policy won't block it. jailoc protects your internal infrastructure and limits file access. It isn't a complete defense against the [lethal trifecta](explanation/threat-model.md).

jailoc runs on macOS with Apple Silicon and on Linux amd64 with KVM. See [Installation](how-to/installation.md) for host requirements.

## Documentation

### Get started

New to jailoc? Start here and run your first workspace in minutes.

- [Getting Started](tutorials/getting-started.md): install jailoc and run your first workspace

### How-to guides

Step-by-step guides for specific tasks once you're up and running.

- [Installation](how-to/installation.md)
- [Workspace Configuration](how-to/workspace-configuration.md)
- [Custom Images](how-to/custom-images.md)
- [Network Access](how-to/network-access.md)
- [Secrets](how-to/secrets.md)
- [SSH & Git Passthrough](how-to/ssh-git-passthrough.md)
- [Sessions](how-to/sessions.md)
- [Coding Agents](how-to/coding-agents.md)
- [Workspace Indicator](how-to/tui-plugin.md)
- [Migrate from jailoc v1](how-to/migrate-from-v1.md)
- [Troubleshooting](how-to/troubleshooting.md)

### Reference

Complete technical descriptions of every CLI command and configuration field.

- [CLI Reference](reference/cli.md)
- [Configuration Reference](reference/configuration.md)
- [Image Resolution](reference/image-resolution.md)
- [Default Image Contents](reference/default-image.md)
- [Overlay Compatibility](reference/overlay-compatibility.md)

### Explanation

Background on how jailoc works and why it's designed the way it is.

- [Overview](explanation/overview.md)
- [Threat Model](explanation/threat-model.md)
- [Architecture](explanation/architecture.md)
- [Network Isolation](explanation/network-isolation.md)
- [Secrets](explanation/secrets.md)

### Development

- [Contributing & Development](development.md)
