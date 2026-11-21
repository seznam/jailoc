![jailoc](hero.jpeg)

# jailoc

Give your coding agent its own computer, not your whole host. jailoc runs OpenCode, Claude Code, GitHub Copilot CLI, OmO or Antigravity CLI in a libkrun micro-VM per workspace, with only the directories you share and only the network your rules allow.

<a id="get-started"></a>

<a class="md-button md-button--primary" href="tutorials/getting-started/">Run your first workspace</a>
<a class="md-button" href="how-to/installation/">Check host requirements and install</a>

jailoc runs on macOS with Apple Silicon and on Linux amd64 (arm64 experimental) with KVM and Landlock. Docker Engine on the host is optional.

## Why jailoc

An agent on your host can reach everything your user account can. jailoc narrows that to what the workspace needs:

- **Only the files you share.** The agent sees the directories you share as workspace paths or mounts, plus read-only snapshots of files you list. Your SSH keys, browser profiles and other projects aren't in the VM.
- **Egress by rule.** A network broker on the host decides every connection. Destinations no allow rule matches are denied, apart from the coding agent's own endpoints, and private and reserved ranges stay closed unless a rule opens them. See [Network Access](how-to/network-access.md).
- **Docker inside the VM.** With `enable_docker`, the Docker daemon runs in the workspace VM, so no host Docker socket is mounted.

!!! warning "What jailoc does not protect against"

    Workspace paths and `rw` mounts are shared read-write, so the agent's edits land on your files at once, including Git hooks that run on the host; [`protect_git`](reference/configuration.md#protect_git) covers only the repositories it applies to. Anything the agent can read can leave through the destinations your rules allow. On Linux, Landlock doesn't confine metadata operations of the VMM process; on macOS the VMM process has no equivalent confinement. [Security Boundaries](explanation/security-boundaries.md#outside-the-boundaries) lists every limit, and the [Threat Model](explanation/threat-model.md) explains why they fall where they do.

## Documentation

### How-to guides

- Install or upgrade: [Installation](how-to/installation.md), [Upgrade an existing installation](how-to/migrate-from-v1.md)
- Shape a workspace: [Workspace Configuration](how-to/workspace-configuration.md), [Custom Images](how-to/custom-images.md)
- Decide what the agent reaches: [Network Access](how-to/network-access.md), [Secrets](how-to/secrets.md), [SSH & Git Passthrough](how-to/ssh-git-passthrough.md)
- Open web apps from the VM in your browser: [Web Routes](how-to/desktop-and-web.md)
- Work with agents: [Sessions](how-to/sessions.md), [Coding Agents](how-to/coding-agents.md), [Workspace Indicator](how-to/tui-plugin.md)
- Fix a failing start: [Troubleshooting](how-to/troubleshooting.md)

### Reference

- Look up a command or field: [CLI Reference](reference/cli.md), [Configuration Reference](reference/configuration.md)
- Find out what a workspace boots: [Image Resolution](reference/image-resolution.md), [Default Image Contents](reference/default-image.md), [Overlay Compatibility](reference/overlay-compatibility.md)

### Explanation

- Judge the risk: [Threat Model](explanation/threat-model.md), [Security Boundaries](explanation/security-boundaries.md), [Alternatives](explanation/alternatives.md)
- See how it works: [Overview](explanation/overview.md), [Architecture](explanation/architecture.md), [Network Isolation](explanation/network-isolation.md), [Secrets](explanation/secrets.md), [Resource Use](explanation/resource-use.md)

### Blog

- [jailoc 2.7: control the request, not just the host](blog/jailoc-2-7.md)
- [Read-only agent configs: your instructions inside the VM, your host untouched](blog/agent-config.md)
- [Taming agent egress: default-deny network rules in jailoc 2.4](blog/network-rules.md)
- [jailoc 2: every agent gets its own computer](blog/jailoc-2.md)

### Development

- [Contributing & Development](development.md)
