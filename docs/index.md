![jailoc](hero.jpeg)

# jailoc

`jailoc` runs coding agents (OpenCode, Claude Code, GitHub Copilot CLI, OmO, Antigravity CLI) in sandboxed micro-VMs so they can work autonomously without touching your host system. Each workspace boots its own lightweight VM with network restrictions, letting you control exactly which directories and internal services the agent can reach.

<a id="get-started"></a>

**Start here:** [Getting Started](tutorials/getting-started.md) installs jailoc and runs your first workspace. jailoc runs on macOS with Apple Silicon and on Linux amd64 (arm64 experimental) with KVM; see [Installation](how-to/installation.md) for host requirements.

## Why jailoc

An agent running directly on your host inherits your user account: every file, every credential, every network endpoint you can reach. A single [prompt injection](explanation/threat-model.md) hidden in a GitHub issue, a web page, or a package README can instruct it to read sensitive data and send it somewhere it shouldn't go. This is the [lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/): private data access + untrusted content + external communication = data theft.

jailoc can't remove the untrusted content, because processing it is the agent's job. It **shrinks the blast radius** by tightening the other two legs:

- **Your files stay out of reach.** The agent runs in its own VM with its own kernel and sees only the directories you share as workspace paths or mounts, plus read-only snapshots of files you list. Your SSH keys, browser profiles and other projects are invisible inside the VM.
- **Your internal network is walled off.** A host-side network broker denies private and reserved IPv4 and IPv6 ranges by default, so a compromised agent can't pivot to your Kubernetes clusters, databases or cloud metadata endpoints. You allowlist only what the agent needs.
- **The Docker socket stays untouched.** Mounting `/var/run/docker.sock` lets an agent escape by starting privileged containers on your host. With `enable_docker`, the Docker daemon runs inside the workspace VM instead.

!!! warning "What jailoc does not protect against"

    The agent can still make outbound requests to the **public internet**. If a prompt injection instructs it to exfiltrate workspace source code to an attacker-controlled public server, jailoc's network policy won't block it. Workspace paths are also shared read-write, so the agent can change your files, including Git hooks that run on the host; [`protect_git`](reference/configuration.md#protect_git) protects hooks only in the repositories it covers. jailoc protects your internal infrastructure and limits file access; it isn't a complete defense against the [lethal trifecta](explanation/threat-model.md).

## Documentation

### How-to guides

- Set up: [Installation](how-to/installation.md), [Upgrade an existing installation](how-to/migrate-from-v1.md)
- Configure a workspace: [Workspace Configuration](how-to/workspace-configuration.md), [Custom Images](how-to/custom-images.md), [Network Access](how-to/network-access.md), [Secrets](how-to/secrets.md), [SSH & Git Passthrough](how-to/ssh-git-passthrough.md)
- Work with agents: [Sessions](how-to/sessions.md), [Coding Agents](how-to/coding-agents.md), [Workspace Indicator](how-to/tui-plugin.md)
- Fix problems: [Troubleshooting](how-to/troubleshooting.md)

### Reference

- [CLI Reference](reference/cli.md)
- [Configuration Reference](reference/configuration.md)
- [Image Resolution](reference/image-resolution.md)
- [Default Image Contents](reference/default-image.md)
- [Overlay Compatibility](reference/overlay-compatibility.md)

### Explanation

- [Overview](explanation/overview.md)
- [Threat Model](explanation/threat-model.md)
- [Security Boundaries](explanation/security-boundaries.md)
- [Architecture](explanation/architecture.md)
- [Network Isolation](explanation/network-isolation.md)
- [Secrets](explanation/secrets.md)
- [Resource Use](explanation/resource-use.md)
- [Alternatives](explanation/alternatives.md)

### Blog

- [jailoc 2.7: control the request, not just the host](blog/jailoc-2-7.md)
- [Read-only agent configs: your instructions inside the VM, your host untouched](blog/agent-config.md)
- [Taming agent egress: default-deny network rules in jailoc 2.4](blog/network-rules.md)
- [jailoc 2: every agent gets its own computer](blog/jailoc-2.md)

### Development

- [Contributing & Development](development.md)
