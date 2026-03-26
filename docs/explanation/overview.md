# How jailoc Works

jailoc is a CLI tool that runs headless [OpenCode](https://opencode.ai) coding agents inside sandboxed Docker Compose environments. Each workspace gets its own isolated pair of containers, a dedicated host port, and network rules that block access to private infrastructure while leaving the public internet open.

The problem it solves is straightforward: running an autonomous coding agent on your machine means trusting it completely. It can read arbitrary files, call any network endpoint, and modify anything your user account can touch. jailoc draws a boundary. The agent runs as an unprivileged user inside a container with capabilities dropped, private network ranges blocked, and its data directory isolated from your host.

## The startup sequence

When you run `jailoc up`, five things happen in order:

**1. Config loading.** `config.Load` reads `~/.config/jailoc/config.toml`. This file defines your workspaces: which directories to mount, which hosts to allow, which image to use. If the file doesn't exist yet, jailoc writes a default config on first run.

**2. Workspace resolution.** `workspace.Resolve` turns a workspace name into everything needed to describe a running environment: the mount paths, the host port (derived as `4096 + alphabetical index` among all workspaces), and the list of allowed hosts and networks the agent may reach.

**3. Image resolution.** `docker.ResolveBaseImage` and `docker.BuildOverlayImage` decide what container image to use. If a workspace sets `image`, that value is used directly and all build steps are skipped. If `defaults.image` is set, it serves as the base for any workspace `dockerfile` overlay, or is used directly when no workspace Dockerfile exists. Otherwise, resolution falls back to a `[base].dockerfile` (local path or HTTP URL), and finally to the Dockerfile embedded in the binary. See [Image Resolution](../reference/image-resolution.md) for the full rules.

**4. Compose generation.** `compose.WriteCompose` renders a `docker-compose.yml` from a Go template and writes it to `~/.cache/jailoc/{workspace}/docker-compose.yml`. The rendered file captures everything workspace-specific: mount paths, port bindings, image reference, environment variables, resource limits.

**5. Container startup.** `docker.Up` hands the rendered compose file to the Docker Compose SDK. Two containers start: the `opencode` container where the agent runs, and the `dind` sidecar that gives the agent its own Docker daemon. See [Container Architecture](container-architecture.md) for details on how they relate.

## Package layout

jailoc's code lives entirely under `internal/` — nothing is exported for use as a library. The packages follow the data flow closely:

| Package | Responsibility |
|---------|---------------|
| `cmd/` | Cobra CLI commands: `up`, `down`, `attach`, `logs`, `status`, `add`, `config` |
| `config/` | TOML parsing, validation, and mutation for `~/.config/jailoc/config.toml` |
| `workspace/` | Name-to-environment resolution: paths, port assignment, CWD detection |
| `compose/` | Docker Compose YAML generation from a Go template |
| `docker/` | Docker Compose SDK and Engine SDK client: image resolution, lifecycle management |
| `embed/` | Embedded assets via `go:embed`: Dockerfile, compose template, entrypoint script, default config |

One deliberate constraint runs through all of this: jailoc never shells out. There's no `exec.Command("docker", ...)` anywhere. Every Docker interaction goes through the Go SDK directly. This keeps the behavior predictable and testable without a Docker CLI binary present.

## Why two containers?

The short answer is capability isolation. If the agent needs Docker (to run tests, build images, spin up databases), it needs a Docker daemon. Sharing the host daemon would let the agent escape its sandbox trivially, since a container with access to the host Docker socket can mount any host path. The `dind` sidecar gives the agent a completely separate Docker daemon that knows nothing about the host. Any containers it starts exist only inside that daemon's scope.

The full two-container design, including how they communicate and what each mounts, is covered in [Container Architecture](container-architecture.md).
