# Learnings

## [2026-03-18] Initial Setup

### Git Branch
- Working branch: `feat/jailoc-cli` (branched from `feat/opencode-sandbox`)
- Previous work on `feat/opencode-sandbox` preserved; do NOT commit to that branch

### Go Module Path
- Use: `github.com/seznam/jailoc` (placeholder — executor adjusts)
- The plan says `gitlab.com/{group}/oc-jail` — use `github.com/seznam/jailoc` as actual value

### Dockerfile Reference
- Current Dockerfile is 128-line multi-stage (builder → runtime), ubuntu:24.04
- Entrypoint: `/usr/local/bin/entrypoint.sh` (copy from repo root, no changes in T0)
- Key versions pinned via ARG + renovate annotations at top

### Docker Compose Reference
- Current compose has `name: jailoc`, services: opencode + dind
- Networks: dind (internal), egress
- Volumes: opencode-data, opencode-cache, dind-certs-ca, dind-certs-client, dind-data
- Port: 4096:4096

### Key Constraints
- Config dir: `~/.config/jailoc/` (hardcode, no XDG library)
- embed assets MUST be in `internal/embed/assets/` (not repo root assets/)
- unexported vars + exported accessor funcs for go:embed
- Load() skips Validate() for auto-created seed config
- Execute(version, commit, date string) version-wiring pattern
- Docker Compose V2 only: `docker compose` subcommand
- Error wrapping: fmt.Errorf("context: %w", err)

## [2026-03-18] Dockerfile rewrite findings

- Homebrew install script fails under root (`Don't run this as root!`); in Docker build it works by running installer as `agent` while preparing `/home/linuxbrew` ownership from root.
- `typescript-language-server` npm package exposes CLI at `lib/cli.mjs` (not `bin/typescript-language-server`), so runtime symlink must point to `.../lib/cli.mjs`.
- `gopls@latest` currently resolves to a build requiring newer Go toolchain via automatic toolchain download; keeping Go runtime at `1.24.1` still succeeds during image build.

## [2026-03-18] Task 1 Complete: Go Module Initialization

### Scaffolding Created
- **go.mod**: Module path `github.com/seznam/jailoc` initialized successfully
- **go.sum**: Locked dependency hashes after `go get` + `go mod tidy`

### Directory Structure
```
cmd/jailoc/
├── main.go (235B) — Version wiring with ldflags pattern

internal/
├── config/
│   └── doc.go — TOML config package declaration
├── embed/
│   ├── doc.go — Asset embedding package declaration
│   └── assets/
│       └── .gitkeep — Placeholder for Task 3 assets
├── workspace/
│   └── doc.go — Workspace management package declaration
├── compose/
│   └── doc.go — Docker Compose generation package declaration
├── docker/
│   └── doc.go — Docker client wrappers package declaration
└── cmd/
    └── root.go (418B) — Minimal Cobra root command with Execute() entrypoint
```

### Dependencies Added
- **github.com/spf13/cobra** v1.10.2 — CLI framework
- **github.com/BurntSushi/toml** v1.6.0 — TOML parser
- **github.com/inconshreveable/mousetrap** v1.1.0 — (transitive, cobra dependency)
- **github.com/spf13/pflag** v1.0.9 — (transitive, cobra dependency)

### Build Verification
✓ `go build ./cmd/jailoc` succeeds (binary: `jailoc`)
✓ `./jailoc --help` outputs: "Manage sandboxed OpenCode Docker environments"
✓ Version string format: "{version} (commit: {commit}, built: {date})"
✓ `go mod tidy` produces no changes (locked state verified)

### Commit Hash
- **feat: init Go module with project structure**
- Files changed: 10 (+67 lines)
- Entry point: `cmd.Execute(version, commit, date)` ← Fixed signature for GoReleaser ldflags

### Key Patterns Established
1. **Version Wiring**: Three separate vars (`version`, `commit`, `date`) in main.go — GoReleaser requires this layout
2. **Package Stubs**: All internal packages have minimal doc.go files — prevents "no Go files" errors
3. **Cobra Root**: `rootCmd.Execute()` in Execute() function — allows version inject via ldflags after cobra init
4. **Module Path**: `github.com/seznam/jailoc` — matches task spec exactly

### No Command Logic
- All packages are declaration-only (doc.go + root.go)
- `go build` succeeds with zero implementation
- Next task adds command substructure

## [2026-03-18] Task 3 Complete: Embedded Docker Assets

### Files Created
- **internal/embed/embed.go** (849B) — Package with 4 go:embed directives + 4 accessor funcs
- **internal/embed/embed_test.go** (1.1K) — 4 unit tests (Dockerfile, ComposeTemplate, Entrypoint, DefaultConfig)
- **internal/embed/assets/Dockerfile** (7.1K) — Exact copy from repo root
- **internal/embed/assets/entrypoint.sh** (1.4K) — Exact copy from repo root
- **internal/embed/assets/docker-compose.yml.tmpl** (2.1K) — Go text/template with {{.WorkspaceName}}, {{.Port}}, {{.Image}}
- **internal/embed/assets/config.toml.default** (250B) — Default TOML config with [workspaces.default] section

### Package Design Pattern
- Unexported vars: `dockerfileBytes`, `composeTemplateStr`, `entrypointBytes`, `defaultConfigBytes`
- Exported funcs: `Dockerfile()`, `ComposeTemplate()`, `Entrypoint()`, `DefaultConfig()`
- Avoids name collisions with stdlib `embed` package (using blank import `import _ "embed"`)
- Test package uses import alias: `jailocembed "github.com/seznam/jailoc/internal/embed"`

### Test Coverage
✓ All 4 tests pass
- `TestDockerfileEmbedded`: Verifies len > 0, contains "FROM"
- `TestComposeTemplateEmbedded`: Verifies len > 0, contains "services:"
- `TestEntrypointEmbedded`: Verifies len > 0, contains "#!/bin/bash"
- `TestDefaultConfigEmbedded`: Verifies len > 0, contains "[workspaces.default]"

### Build Verification
✓ `go test ./internal/embed/...` — 4 passed
✓ `go build ./...` — Success (entire project)

### Commit
- **feat(embed): add embedded Docker assets**
- Files changed: 7 (+371 lines)
- Removes .gitkeep placeholder from assets/

### Key Patterns Established
1. **go:embed with relative paths**: Must be relative to package dir, stored in `internal/embed/assets/`
2. **Template format**: docker-compose.yml.tmpl is valid YAML with Go template syntax ({{.VarName}})
3. **Asset copying**: Dockerfile and entrypoint.sh are exact copies from repo root (no modifications)
4. **Accessor pattern**: Unexported variables + exported functions prevents any accidental mutation

### Next Task Dependency
Task 5 (GenerateCompose) will call `ComposeTemplate()` and `text/template.Execute()` to render the docker-compose.yml

## [2026-03-18] Task 4 Complete: Workspace Resolution + Port Allocation

- `internal/workspace/workspace.go` now resolves `config.Workspaces` by name, expands `~`, normalizes to absolute paths, and computes deterministic ports via alphabetical workspace ordering (`4096 + index`).
- `ResolveFromCWD` uses literal prefix matching with explicit path-boundary handling so `/foo` does not match `/foobar`.
- `BuildContext` is expanded only when non-empty; empty value stays empty.
- `PortForWorkspace` returns `-1` when workspace name is missing.
- Test suite added in `internal/workspace/workspace_test.go` with all 13 required cases (valid/nonexistent resolve, CWD match/no-match, alphabetical and single default ports, tilde+spaces expansion, build_context behavior, prefix-boundary checks, and direct `MatchesCWD`).
- Verification: `go test ./internal/workspace/...` passes and evidence captured in `.sisyphus/evidence/task-4-tests.txt`.

## 2026-03-18 T5 compose generation
- Compose template now mounts each resolved workspace path to /workspace/{{base path}} via a registered template func `base` backed by filepath.Base.
- OPENCODE_SERVER_PASSWORD is rendered directly from ComposeParams.OpenCodePassword, so empty values render as empty string without shell interpolation.
- Workspace-scoped named volumes use opencode-data-<workspace> and opencode-cache-<workspace> in both service mounts and top-level volume declarations.

## 2026-03-18 T6 docker package
- `internal/docker/docker.go` now provides Docker Compose wrappers (`Up`, `Down`, `IsRunning`, `Logs`, `Exec`) using `docker compose -f <composeFile> ...` with `exec.CommandContext` and configurable stdio streams.
- `IsRunning` uses NDJSON parsing from `docker compose ps --format json`; helper `parseServiceState` scans line-by-line and returns true only when `Service=="opencode"` and `State=="running"`.
- Base image resolution chain is implemented as requested: local base override Dockerfile build (`jailoc-base:local`) → registry pull (`<repo>:<version>`) → embedded Dockerfile fallback build (`jailoc-base:embedded`) with stderr warning when pull fails/fallback is used.
- Workspace image layering is split into `ApplyWorkspaceLayer(ctx, base, workspaceName)`; if `~/.config/jailoc/<workspace>.Dockerfile` exists it builds `jailoc-<workspace>:latest` with `--build-arg BASE=<base>`, otherwise it returns the base image unchanged.
- Unit tests in `internal/docker/docker_test.go` focus on pure logic only (constructor field wiring, ps-output parsing, config override path detection + existence checks) and avoid mocking `os/exec` or requiring Docker daemon access.

- Implemented `up` command flow with docker preflight check, compose cache generation under `~/.cache/jailoc/{workspace}/`, compose write, and compose up startup path.
- Added exported helper `ResolveAndLayerImage(ctx, cfg, ws, version)` to compose the 4-tier image resolution chain by combining `docker.ResolveImage` and `docker.ApplyWorkspaceLayer`.
- Added exported shared cache path helper `ComposeCacheDir(workspace)` returning a trailing-separator path for cross-command reuse.

## [2026-03-18] Task 17 integration smoke tests

- Integration smoke tests live in `internal/integration_test.go` with `//go:build integration` and `package integration_test` so they are excluded from default `go test` runs.
- `TestMain` builds the CLI binary (`go build -o <tmp>/jailoc ./cmd/jailoc`) and all test scenarios invoke CLI behavior through subprocess execution, never internal command imports.
- Each scenario uses its own temp HOME and writes config under `~/.config/jailoc/config.toml`, matching `config.ConfigPath()` behavior while avoiding cross-test state leakage.
- Docker-dependent scenarios gate on daemon availability and skip when Docker/registry prerequisites are missing, keeping integration suite deterministic in constrained environments.
