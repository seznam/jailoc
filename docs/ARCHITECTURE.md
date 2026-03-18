# Architecture

This document describes the internal structure of jailoc for contributors and anyone curious about how it works under the hood.

## Package Overview

```
cmd/jailoc/main.go          Entry point — passes ldflags (version, commit, date) to cmd.Execute()
internal/
  cmd/                       Cobra CLI commands (root, up, down, attach, logs, status, config, add)
  config/                    TOML config parsing, validation, mutation (~/.config/jailoc/config.toml)
  workspace/                 Workspace resolution, path matching, port assignment
  compose/                   Docker Compose YAML generation from Go template
  docker/                    Docker Compose SDK + Engine SDK client (image build/pull, compose up/down/exec)
  embed/                     go:embed assets (Dockerfile, compose template, entrypoint.sh, default config)
```

All packages live under `internal/` — nothing is exported.

## Data Flow

A typical `jailoc up` follows this path:

```
                      ┌─────────────┐
                      │  config.Load │  Read ~/.config/jailoc/config.toml
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │  workspace   │  Resolve name → paths, port, allowed hosts/networks
                      │  .Resolve()  │  Port = 4096 + alphabetical index of workspace name
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   docker     │  Image resolution (4-step cascade):
                      │ .ResolveImage│  1. Local Dockerfile override → build
                      │              │  2. Registry pull → {repo}:{version}
                      │              │  3. Embedded fallback Dockerfile → build
                      │              │  4. Workspace layer → {name}.Dockerfile on top
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   compose    │  Render docker-compose.yml from embedded template
                      │ .WriteCompose│  Write to ~/.cache/jailoc/{workspace}/docker-compose.yml
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   docker     │  Compose SDK: Up() starts the services
                      │   .Up()      │  Two containers: opencode + dind sidecar
                      └─────────────┘
```

## Key Packages

### `config`

Reads and writes `~/.config/jailoc/config.toml`. Auto-creates with defaults on first run.

- **`Config`** struct: `Image.Repository` (registry URL) + `Workspaces` map (name → paths, allowed hosts/networks, build context)
- **Validation**: workspace names must match `[a-z0-9-]+`, paths must be non-empty, CIDR ranges validated with `net.ParseCIDR`
- **Mutation**: `AddPath()` appends a directory to a workspace's path list and re-encodes the TOML

### `workspace`

Resolves a workspace name into a `Resolved` struct with expanded absolute paths, computed port, and network config.

- **Port assignment**: all workspace names sorted alphabetically, port = `4096 + index`
- **CWD matching**: `ResolveFromCWD()` finds which workspace owns the current directory (used by bare `jailoc` command)
- **Path expansion**: `~` is expanded to `$HOME`

### `compose`

Renders `docker-compose.yml` from an embedded Go template (`internal/embed/assets/docker-compose.yml.tmpl`).

Template variables: workspace name, port, image tag, mount paths, allowed hosts/networks, OpenCode password.

The generated file goes to `~/.cache/jailoc/{workspace}/docker-compose.yml` — this is what the Docker Compose SDK reads.

### `docker`

Docker Compose SDK (`github.com/docker/compose/v5`) and Engine SDK (`github.com/docker/docker/client`).

- **Lazy init**: `NewClient()` returns `*Client` with no error — SDK clients initialize on first use via `sync.Once`
- **Compose operations**: `Up`, `Down`, `IsRunning`, `Logs`, `Exec` — all go through `api.Compose` interface
- **Image operations**: `ResolveImage` (pull or build base), `ApplyWorkspaceLayer` (build workspace Dockerfile on top)
- **No shelling out**: zero `exec.Command` calls — everything uses Go SDK

### `embed`

`go:embed` directives for assets baked into the binary:

| Asset | Purpose |
|-------|---------|
| `Dockerfile` | Fallback base image when registry pull fails |
| `docker-compose.yml.tmpl` | Go template for compose file generation |
| `entrypoint.sh` | Container entrypoint: iptables setup → chown → drop to UID 1000 |
| `config.toml.default` | Default config written on first run |

### `cmd`

Cobra commands. Each file registers a single command in `init()`.

| File | Command | Key behavior |
|------|---------|------|
| `root.go` | `jailoc` (bare) | CWD detection → prompt to add → start if not running → attach |
| `up.go` | `jailoc up` | Image resolution → compose generation → SDK up |
| `down.go` | `jailoc down` | SDK down |
| `attach.go` | `jailoc attach` | Runs `opencode attach` on the host (exec, not SDK) |
| `logs.go` | `jailoc logs` | SDK logs with `writerLogConsumer` |
| `status.go` | `jailoc status` | Iterates all workspaces, checks running status |
| `add.go` | `jailoc add` | Adds CWD to workspace path list via `config.AddPath` |
| `config_cmd.go` | `jailoc config` | Prints resolved config |

## Container Architecture

Two services per workspace, connected via an internal Docker network:

```
┌────────────────────────────────────────────────────┐
│  Docker Compose project: jailoc-{workspace}        │
│                                                    │
│  ┌──────────────────────┐  ┌────────────────────┐  │
│  │  opencode             │  │  dind (privileged) │  │
│  │                       │  │                    │  │
│  │  UID 1000 (agent)     │  │  Docker daemon     │  │
│  │  opencode serve       │  │  TLS on :2376      │  │
│  │  :4096 → host port    │  │                    │  │
│  │                       │  │  Shared volumes:   │  │
│  │  Mounts:              │  │  - certs (TLS)     │  │
│  │  - /workspace/* (rw)  │  │  - docker data     │  │
│  │  - ~/.config/oc (ro)  │  │                    │  │
│  │  - /etc/jailoc (ro)   │  │                    │  │
│  └──────────┬────────────┘  └────────────────────┘  │
│             │ tcp://dind:2376 (TLS)                  │
│             └──── dind network (internal) ───────────│
│                                                      │
│             ─── egress network (external) ───────────│
└──────────────────────────────────────────────────────┘
```

**Network isolation** (entrypoint.sh):
1. ACCEPT rules for DinD, host gateway, and configured allowed hosts/networks
2. DROP rules for RFC 1918, link-local, and CGNAT ranges
3. Public internet stays open

**Privilege drop** (entrypoint.sh):
1. Runs as root to set up iptables and fix ownership
2. `setpriv --reuid=1000 --regid=1000 --inh-caps=-all --no-new-privs` before exec

## CI/CD

GitLab CI (`.gitlab-ci.yml`):

| Stage | Job | Trigger |
|-------|-----|---------|
| build | `build` | Every branch/tag — `go build` with ldflags |
| test | `test` | Every branch/tag — `go test` + `go vet` |
| test | `integration-test` | Tags matching `v*` — `go test -tags=integration` with DinD |
| release | `release` | Tags matching `v*` — GoReleaser → GitLab Release |
| image-push | `push-base-image` | Tags matching `v*` — Build + push base Docker image to registry |

GoReleaser (`.goreleaser.yml`): builds for linux/darwin × amd64/arm64, static binaries (`CGO_ENABLED=0`), changelog from GitLab.

## Contributing

### Prerequisites

- Go 1.24+
- Docker with Compose V2

### Development

```bash
# Build
go build ./cmd/jailoc

# Unit tests
go test ./...

# Integration tests (requires Docker)
go test -tags=integration ./internal/...

# Lint
go vet ./...
```

### Project Conventions

- **Module**: `github.com/seznam/jailoc`
- **Commits**: `type(scope): description` — types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`
- **Error wrapping**: always `fmt.Errorf("context: %w", err)`, never bare `err`
- **No custom error types** — `fmt.Errorf` wrapping throughout
- **No logging library** — `fmt.Printf` for user output, errors propagate up
- **Single file per package concern** — `docker.go`, `compose.go`, `workspace.go`, `config.go`
- **Tests**: `*_test.go` next to source, `integration_test.go` uses build tag `integration`
