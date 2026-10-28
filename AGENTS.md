# AGENTS.md

## Project Overview

`jailoc` — CLI tool that runs coding agents (OpenCode, Claude Code, GitHub Copilot CLI, OmO; one per workspace) in sandboxed libkrun micro-VMs. Each workspace gets its own VM with virtio-fs directory shares, a host-side userspace network broker that denies private networks, and SSH-based agent sessions.

**Module**: `github.com/seznam/jailoc`
**Language**: Go 1.26+
**Key deps**: stacklok/go-microvm (libkrun), gvisor.dev/gvisor (netbroker stack), docker/docker (image builds, migrate), google/go-containerregistry, golang.org/x/crypto/ssh, BurntSushi/toml, spf13/cobra

**Hosts**: darwin/arm64 (Hypervisor.framework) and linux/amd64 (KVM). jailoc downloads a pinned go-microvm runner + libkrun + libkrunfw runtime and verifies its SHA-256 (`internal/vm/runtime.go`).

## Architecture

```
cmd/jailoc/main.go          Entry point → cmd.Execute(version, commit, date)
cmd/jailoc-guest/           VM init + SSH server inside the guest (embedded via internal/guestbin)
cmd/jailoc-netbroker/       Host-side network broker process, one per workspace VM
internal/
  cmd/                       Cobra CLI: root (attach/new/continue sessions) + add, config, down, logs, migrate, reload, restart, sessions, status, up
  agent/                     Built-in coding agent harnesses (opencode, claude, copilot, omo): npm package, session flags, env, persisted dirs, default mounts
  config/                    TOML config parsing, validation (rejects v1 settings), mutation (~/.config/jailoc/config.toml), default config content
  workspace/                 Workspace name → resolved paths, allowed hosts/networks, CWD matching
  docker/                    Docker Engine SDK — image resolution/build on the host, v1 volume export for migrate
  embed/                     go:embed assets (Dockerfile, TUI plugin)
  vm/                        go-microvm/libkrun VM lifecycle, runtime download, image export into rootfs
  guest/                     jailoc-guest: init, SSH sessions, secrets, Docker in the VM
  guestbin/                  go:generate-embedded jailoc-guest binaries
  netbroker/                 gVisor userspace network stack + IPv4/IPv6 egress policy + filtered DNS
  brokerproc/                Starts a jailoc-netbroker process for one workspace
  vmnet/, logging/, update/  Supporting packages
  vmintegration/             //go:build vmintegration — boots real VMs
docs/                        MkDocs documentation (Diátaxis framework, see docs/AGENTS.md)
```

### Data flow (`jailoc up`)

1. `config.Load` → read `~/.config/jailoc/config.toml`; v1 settings fail validation with a hint to run `jailoc migrate`
2. `workspace.Resolve` → name → paths, mounts, snapshots, allowed hosts/networks
3. Validate secret sources, prepare CA bundle and TUI plugin config, compute virtio-fs shares and staged `snapshots`
4. `vmRuntime` → download/verify the pinned VM runtime (or use `[vm] runner` / `lib_dir`)
5. `buildWorkspaceImage` → build the image with host Docker (or a builder VM with `isolated_build`) and unpack it into the VM image cache
6. `startSupervisor` → boot the workspace VM (jailoc-guest as init) with its netbroker; persistent home under `~/.cache/jailoc-vm/workspaces/<ws>/home`
7. `secretsPayload` (read on the host before boot, incl. `from_command`) → `deliverSecrets` over SSH; the guest writes file secrets to `/run/secrets` (tmpfs) and env secrets to the session environment

If the VM is already running, `up` reloads it instead (`internal/cmd/vmreload_unix.go`). Sessions attach over SSH; there is no published agent port or server password.

### Network

Guest IPv4 192.168.127.2/24 (gateway .1), IPv6 fd00:127::2/64. All traffic goes through the netbroker: public destinations allowed, private/reserved denied unless listed in `allowed_networks` or resolved from `allowed_hosts`. See `internal/netbroker/policy.go` and `policy_ipv6.go`.

## Conventions

### Error handling
- Always wrap: `fmt.Errorf("context: %w", err)` — never return bare `err`
- No custom error types — `fmt.Errorf` wrapping everywhere
- No logging library — `fmt.Printf` for user output, errors propagate up the stack

### Code organization
- One file per package concern: `docker.go`, `workspace.go`, `config.go`
- Separate files for distinct concerns within a package (e.g. `fetch.go` for HTTP fetching in `docker/`)
- All packages under `internal/` — nothing exported
- Prefer Go SDKs over `exec.Command` shellouts; shellouts are limited to the VM runner, broker process, and guest-side tools
- No external dependencies for stdlib-solvable problems (e.g. `net/http` for fetching, not a third-party client)

### Validation rules
- Workspace names: `^[a-z0-9-]+$`
- Forbidden mount prefixes: `/home/agent`, `/usr`, `/etc`, `/var`, `/bin`, `/sbin`, `/lib`, `/lib64`
- CIDR validation via `net.ParseCIDR`
- URL fields (e.g. `dockerfile`): validate scheme (`http`/`https`) and non-empty host — reject malformed URLs like `http:///path`
- Path expansion: `~` → `$HOME` (skip URL fields — they must not be expanded)

### Embedded assets (`internal/embed/assets/`)
- `Dockerfile` — embedded fallback base image for the VM rootfs (no entrypoint/CMD; jailoc-guest is the init)
- `tui.js` — built TUI plugin JS (from `plugin/index.jsx` via the Babel-based Solid transform in `plugin/build.mjs`)
- `tui-plugin.json` — minimal package.json for the embedded TUI plugin

### TUI plugin rebuild
When `plugin/index.jsx` changes, rebuild and update the embedded copy:
```sh
npm run build
cp dist/tui.js internal/embed/assets/tui.js
```
`npm run build` runs the Node-based Babel/Solid pipeline in `plugin/build.mjs`. The embedded `tui.js` must stay in sync with the plugin source — `go:embed` picks it up at compile time.

## Testing

- Unit tests: `*_test.go` beside source, `t.Parallel()`, table-driven with `t.Run`
- Docker integration tests: build tag `//go:build integration` (e.g. `internal/docker/docker_integration_test.go`), need a Docker daemon, not run in CI
- VM integration tests: `internal/vmintegration/`, build tag `vmintegration`, need KVM or macOS plus a go-microvm runtime (`JAILOC_VM_RUNNER`, `JAILOC_VM_LIBDIR`); run by `.github/workflows/vm-integration.yml`
- Custom `assertContains` helper — no testify
- No mocks, fixtures, or testdata directories
- `t.Setenv()` is incompatible with `t.Parallel()` — tests using `t.Setenv` must NOT be parallel
- `httptest.NewServer` in parallel subtests: use `t.Cleanup(ts.Close)`, NOT `defer ts.Close()` (defer runs before parallel subtests start)

## Development environment

`dev/Dockerfile.jailoc` is a workspace overlay for developing jailoc
inside a jailoc workspace. It extends the base image with the Go toolchain, gopls,
and golangci-lint matching CI.

Add to `~/.config/jailoc/config.toml`:

```toml
[workspaces.jailoc]
paths = ["/path/to/jailoc"]
dockerfile = "https://raw.githubusercontent.com/seznam/jailoc/main/dev/Dockerfile.jailoc"
```

`jailoc up jailoc` starts a VM with Go, gopls, and golangci-lint on PATH.
Run `go build`, `go test ./...`, and `golangci-lint run` inside as usual.

## CI/CD

GitHub Actions workflows:
- **CI** (`.github/workflows/ci.yml`): runs on push/PR to main — `go test`, golangci-lint v2.12.2, and a TUI plugin sync check (`dist/tui.js` vs embedded copy)
- **VM Integration** (`.github/workflows/vm-integration.yml`): PRs/pushes to main touching VM code — boots real workspace VMs on an x64 Linux runner with KVM
- **VM runtime** (`.github/workflows/vm-runtime.yml`): `vm-runtime-*` tags — builds and publishes the VM runtime bundle
- **Release** (`.github/workflows/release-please.yml`): runs on push to main — Release Please opens a Release PR; on merge, creates `v*` tag + GitHub Release, then GoReleaser runs `go generate ./internal/guestbin` and publishes binaries (Linux/Darwin × amd64/arm64, `CGO_ENABLED=0`)
- **Re-release** (`.github/workflows/release.yml`): manual `workflow_dispatch` fallback — re-runs GoReleaser for an existing tag
- **Docs** (`.github/workflows/release-please.yml`): runs as part of the release process — zensical + mkdocs-macros-plugin → GitHub Pages

## Before committing

Run these locally and fix all failures before staging a commit:

```sh
go build ./...
go test ./...
go vet ./...
golangci-lint run
```

## Commits

`type(scope): description` — types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`. Imperative mood.

## Documentation

Docs follow the [Diátaxis](https://diataxis.fr/) framework. See `docs/AGENTS.md` for conventions.

When adding or changing user-facing features, update the corresponding docs in `docs/`:
- `docs/tutorials/` — learning-oriented walkthroughs (Getting Started)
- `docs/how-to/` — goal-oriented guides (installation, workspace config, custom images, network access, sessions, migration from v1)
- `docs/reference/` — exact technical descriptions (CLI, configuration fields, image resolution)
- `docs/explanation/` — understanding-oriented context (overview, threat model, architecture, network isolation, secrets)

Keep docs in sync with code changes — a feature without updated docs is incomplete.

`docs/llms.txt` is an [llmstxt.org](https://llmstxt.org/) file listing all doc pages with descriptions. When adding, removing, or renaming a doc page, update `llms.txt` to match.
