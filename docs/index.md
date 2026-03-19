![jailoc](hero.jpeg)

# jailoc

Manage sandboxed Docker Compose environments for headless OpenCode coding agents.

## 🤔 What is this?

`jailoc` wraps OpenCode agents in isolated Docker containers so they can run autonomously without touching your host system. Each workspace gets its own sandboxed environment with network isolation that blocks private networks by default, letting you control exactly which internal services the agent can reach. You configure which directories to mount as workspaces, which hosts to allowlist, and the agent runs inside with your OpenCode config available read-only.

## ⚙️ How it works

When you run any jailoc command, it reads `~/.config/jailoc/config.toml`, creating it with defaults if it doesn't exist yet.

**🗂️ Workspace resolution** matches workspace paths against the current working directory. Port numbers are computed by sorting all workspace names alphabetically and assigning `4096 + index`.

**🐳 Image resolution** follows four steps in order:
1. If `~/.config/jailoc/Dockerfile` exists, build it as the base.
2. Otherwise, try pulling `{repository}:{version}` from the registry.
3. If the pull fails, build from the embedded fallback Dockerfile (baked into the binary at compile time).
4. If `~/.config/jailoc/{workspace}.Dockerfile` exists, build a workspace layer on top of whichever base was resolved.

**📄 Compose file generation** — jailoc renders a `docker-compose.yml` from an embedded Go template and writes it to `~/.cache/jailoc/{workspace}/docker-compose.yml`. The embedded Compose SDK loads this file directly — no host `docker compose` CLI is invoked.

**🔄 Docker Compose orchestration** — two services are managed via the [Compose Go SDK](https://github.com/docker/compose): the `opencode` service (the agent container) and a `dind` sidecar that provides an isolated Docker daemon. The agent communicates with the DinD daemon over TLS using a shared named volume for certificates. No host Docker socket is mounted.

**🚪 Entrypoint** — the container starts as root so it can set up iptables rules and chown the data volume. It then drops to UID 1000 (`agent`) via `setpriv --inh-caps=-all --no-new-privs` before executing the OpenCode server process.

**💾 Volume mounts** — workspace paths are bind-mounted at their original absolute path (host path = container path). OpenCode config directories (`~/.config/opencode`, `~/.opencode`, `~/.claude`, `~/.agents`) are mounted read-only. An isolated named volume holds the OpenCode data directory, keeping the agent's database and auth tokens separate from the host.
