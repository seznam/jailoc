# Security Policy

## Supported versions

Security fixes go into the latest release. Upgrade to it before reporting, and check whether the issue still reproduces there.

## Reporting a vulnerability

Report vulnerabilities privately through [GitHub security advisories](https://github.com/seznam/jailoc/security/advisories/new). Don't open a public issue, pull request or discussion for a suspected vulnerability.

Include:

- the jailoc version (`jailoc --version`) and host platform (macOS arm64, Linux amd64 or arm64, kernel version);
- the relevant part of `config.toml`, with secret sources removed;
- steps to reproduce, ideally a command the agent or guest root can run inside the VM;
- what you expected and what happened.

We confirm the report, agree on a fix and a disclosure date with you, and credit you in the advisory unless you ask otherwise.

## Scope

jailoc treats everything inside a workspace VM as untrusted, including guest root and the guest kernel. The security boundaries are described in [Security Boundaries](https://seznam.github.io/jailoc/explanation/security-boundaries/) and [Threat Model](https://seznam.github.io/jailoc/explanation/threat-model/).

In scope:

- escaping the workspace VM, or running code on the host from inside the guest;
- reading or writing host files outside the shares, snapshots and secrets configured for the workspace (on Linux, beyond the documented Landlock limits);
- writing to a read-only mount or snapshot from the guest;
- reaching a destination the network policy denies, including through DNS, IPv6 or the in-VM Docker daemon;
- changing or bypassing the broker policy from the guest;
- reaching another workspace's files, secrets, sessions or network allowances without configuration that shares them;
- undoing `protect_git` from inside the VM;
- secret values written to host disk, image layers, build caches or logs;
- the host trusting data the guest controls, such as files in the internal share;
- tampering with releases, the embedded VM runtime or the build pipeline.

Out of scope, because these are documented limitations:

- the agent sending data it can read to the public internet;
- the agent reading shared directories, snapshots or configured secrets, or becoming root in the guest without `protect_git`;
- access gained through `host_ports`, a forwarded SSH agent, a shared agent profile or a read-write agent configuration mount;
- Dockerfile build steps on the host Docker daemon;
- virtio-fs confinement on macOS, and metadata operations on host files outside the shares on Linux;
- prompt injection against the coding agent itself;
- resource exhaustion inside the VM.

Vulnerabilities in libkrun, libkrunfw, go-microvm or gVisor belong to their upstream projects. Report them there, and tell us privately if a jailoc release is affected.
