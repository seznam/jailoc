# Contributing

By participating in this project you agree to follow the [Code of Conduct](CODE_OF_CONDUCT.md). Report security vulnerabilities privately as described in [SECURITY.md](SECURITY.md), not in public issues or pull requests.

## Prerequisites

- Go 1.26+
- Docker (builds workspace images, required for Docker integration tests)
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2.15.0
- Node.js 24 (only when changing the TUI plugin in `plugin/`)

## Local setup

```bash
git clone https://github.com/seznam/jailoc
cd jailoc
go generate ./internal/guestbin
go build ./cmd/jailoc
```

`go generate ./internal/guestbin` builds `jailoc-guest`, the VM init, and embeds it into `jailoc`. Without it the binary builds but can't start workspaces. Source builds don't include the VM runtime either; see [Build from source](https://seznam.github.io/jailoc/how-to/installation/#build-from-source) for how to provide it.

## Testing

Unit tests run without any external dependencies:

```bash
go test ./...
```

Docker integration tests (`internal/docker/`, build tag `integration`) need a running Docker daemon:

```bash
go test -tags integration ./internal/docker/
```

VM integration tests (`internal/vmintegration/`, build tag `vmintegration`) boot real workspace VMs. They need KVM or macOS and a go-microvm runtime pointed to by `JAILOC_VM_RUNNER` and `JAILOC_VM_LIBDIR`:

```bash
JAILOC_VM_RUNNER=/path/to/go-microvm-runner JAILOC_VM_LIBDIR=/path/to/lib \
  go test -tags vmintegration -count=1 -timeout 50m ./internal/vmintegration/
```

CI runs them on pull requests that touch VM code.

Write tests for every new behavior. Unit tests belong next to the source file they cover (`*_test.go`), use `t.Parallel()`, and follow table-driven style with `t.Run`.

## TUI plugin

After changing `plugin/opencode1.jsx` or `plugin/opencode2.jsx`, rebuild the plugin and update the embedded copies. CI fails when they differ:

```bash
npm ci
npm run build
cp dist/tui-opencode1.js dist/tui-opencode2.js internal/embed/assets/
```

## Before committing

```bash
go build ./...
go test ./...
go vet ./...
golangci-lint run
```

CI runs `go test` and `golangci-lint` (including gosec, staticcheck and gocritic) on every push and PR; a failing test or lint blocks merge. CodeQL and `govulncheck` report findings to GitHub code scanning.

## Documentation

User-facing changes need matching updates in `docs/`, which follow the [Diátaxis](https://diataxis.fr/) framework. See [docs/AGENTS.md](docs/AGENTS.md) for conventions, and update `docs/llms.txt` when you add, remove or rename a page.

## Semantic commits

Single line, imperative mood:

```
type(scope): description
```

Types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`. No body, no footer, no `Signed-off-by`.

```
feat(workspace): add port collision detection
fix(docker): handle nil client on early exit
test(config): cover invalid CIDR validation
```

Pull requests are squash-merged, so the PR title becomes the commit on `main` and must follow the same format. [Release Please](https://github.com/googleapis/release-please) builds the changelog from it.

## Pull requests

1. Branch from `main`: `git checkout -b type/short-description`
2. Keep changes focused — one logical change per PR
3. Fill in the pull request template
4. All CI checks must pass and all review threads must be resolved
5. Open the PR against `main`
