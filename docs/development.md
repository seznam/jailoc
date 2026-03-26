# Development

This page is for contributors. It covers how to build and test the project locally, the CI/CD pipeline, what ships inside the default container image, and the coding conventions the codebase follows.

## Prerequisites

- Go 1.26+
- Docker with Compose V2

## Building and testing

```bash
# Build the binary
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run integration tests (requires a running Docker daemon)
go test -tags=integration ./internal/...

# Lint
go vet ./...
```

## CI/CD pipeline

The pipeline runs on GitHub Actions. Most workflows trigger on every branch and pull request; release workflows are gated on version tags (`v*`).

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| CI | Every branch/PR — runs on `push` and `pull_request` events | Build (`go build` with ldflags), test (`go test` + `go vet`), and lint with golangci-lint |
| Release | Tags matching `v*` — runs on `push` with `v*` ref | GoReleaser publishes static binaries and generates a GitHub Release with changelog |
| Docs | Tags matching `v*` — runs on `push` with `v*` ref | Builds documentation with MkDocs and deploys to GitHub Pages |

Integration tests run as part of the CI workflow on `v*` tags with `go test -tags=integration`. GoReleaser builds static binaries (`CGO_ENABLED=0`) for `linux/darwin` x `amd64/arm64`. Configs live in `.goreleaser.yml` and `.github/workflows/`.

## Default container contents

The embedded Dockerfile defines an Ubuntu 24.04 base image. Exact versions are pinned there and kept up to date by Renovate.

| Category | Tools |
|----------|-------|
| Runtimes | Go, Node.js, Bun, Python 3 + uv |
| Package managers | npm, Yarn (via corepack), Homebrew |
| Language servers | gopls, typescript-language-server, pyright, yaml-language-server, bash-language-server, jsonnet-language-server, helm-ls |
| CLI tools | Docker CLI, ripgrep, fd, fzf, jq, vim, git, openssh-client |
| Agent stack | OpenCode, oh-my-openagent |

Source: [`internal/embed/assets/Dockerfile`](https://github.com/seznam/jailoc/blob/master/internal/embed/assets/Dockerfile)

## Project conventions

### Module

```
github.com/seznam/jailoc
```

### Commit format

```
type(scope): description
```

Valid types: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`. Use imperative mood in the description.

### Error handling

Always wrap errors with context. Never return a bare `err`.

```go
// correct
return fmt.Errorf("resolve workspace: %w", err)

// wrong
return err
```

There are no custom error types in this codebase. `fmt.Errorf` wrapping is the only pattern used. There is no logging library either: `fmt.Printf` handles user-facing output, and errors propagate up the call stack.

### File organization

One file per package concern: `docker.go`, `compose.go`, `workspace.go`, `config.go`. When a concern grows large enough to split, create a new file with a descriptive name (e.g. `fetch.go` for HTTP fetching inside the `docker` package). All packages live under `internal/` — nothing is exported.

### Testing

Unit tests live beside their source files as `*_test.go`. Integration tests live in `internal/integration_test.go` and require the `//go:build integration` build tag. Tests use `t.Parallel()` and a table-driven style with `t.Run`. There are no mocks, fixtures, or testdata directories.
