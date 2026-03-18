# Decisions

## [2026-03-18] Architecture Decisions

### Version Wiring
- `cmd/jailoc/main.go` declares `version`, `commit`, `date` vars (for GoReleaser ldflags)
- `main.go` calls `cmd.Execute(version, commit, date)`
- `internal/cmd/root.go` has `Execute(version, commit, date string) error`

### Embed Layout
- `internal/embed/assets/Dockerfile` — embedded fallback Dockerfile
- `internal/embed/assets/docker-compose.yml.tmpl` — compose template
- `internal/embed/assets/entrypoint.sh` — copy of ./entrypoint.sh
- `internal/embed/assets/config.toml.default` — default config template

### Config Auto-Creation
- Missing config → CreateDefault() → parse → return WITHOUT Validate()
- Existing config → parse → Validate() → return or error

### Image Tag
- Base: `{repository}:{version}` where version = jailoc release version
- Workspace: `jailoc-{workspace}:latest`

### Port Allocation
- Alphabetical sort of workspace names → index → 4096+index
- Default workspace "default" will be first or second depending on alphabetical sort

### Build Context
- Default: empty temp dir (for workspace Dockerfile layering)
- Overridable per-workspace via `build_context = "..."` in config

- `up` preflight uses a temporary minimal compose file with `docker compose ps` via `Client.IsRunning` to verify docker daemon reachability before workspace-specific operations.
- Missing compose file during already-running check is treated as "not running" (first-run compatible) by matching docker compose missing-file error text and continuing startup.
- CLI version is exposed to command package via `appVersion` set in `Execute(version, commit, date)` for image tag resolution.

## [2026-03-18] Task 17 test strategy decisions

- Integration tests use external package `integration_test` and execute `jailoc` binary subprocesses to validate real CLI wiring and avoid import cycles with `internal/cmd`.
- Per-test isolation is enforced through dedicated temp HOME directories and explicit cleanup (`jailoc down` + filesystem removal) via `t.Cleanup`.
- Required verification commands are evidenced in `.sisyphus/evidence/task-17-integration-compile.txt`, including both exclusion (without tag) and inclusion (with tag) behavior.
