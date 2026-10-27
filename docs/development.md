# Development

This page is for contributors. It covers how to build and test the project locally, the CI/CD pipeline, what ships inside the default workspace image, and the coding conventions the codebase follows.

## Prerequisites

- Go 1.26+
- Docker Engine, for building workspace images
- macOS on Apple Silicon or Linux amd64 with KVM, for running workspace VMs

## Building and testing

```bash
# Build the binary
go build ./cmd/jailoc

# Embed jailoc-guest, the VM init (release builds do this automatically)
go generate ./internal/guestbin
go build ./cmd/jailoc

# Verify all packages compile (pre-commit)
go build ./...

# Run unit tests
go test ./...

# Run the VM integration tests (needs KVM or macOS, a go-microvm runner and host Docker)
JAILOC_VM_RUNNER=/path/to/go-microvm-runner JAILOC_VM_LIBDIR=/path/to/libs \
  go test -tags vmintegration -v -timeout 45m ./internal/vmintegration/

# Lint
go vet ./...
golangci-lint run
```

## CI/CD pipeline

The pipeline runs on GitHub Actions. Most workflows trigger on every branch and pull request; release workflows are gated on version tags (`v*`).

| Workflow | Trigger | Purpose |
|----------|---------|---------|
| CI | Pushes and pull requests to `main` | Unit tests (`go test`), golangci-lint, and a check that the embedded TUI plugin matches its source |
| Release | Tags matching `v*` — runs on `push` with `v*` ref | GoReleaser publishes static binaries and generates a GitHub Release with changelog |
| Docs | Tags matching `v*` — runs on `push` with `v*` ref | Builds documentation with MkDocs and deploys to GitHub Pages |
| Pre-release | Manual (`workflow_dispatch`) with a ref and a version such as `1.20.0-rc.1` | Tags the ref and publishes GoReleaser binaries as a GitHub pre-release; no docs deploy, no release-please involvement |
| VM Integration | PRs and pushes to `main` touching VM code, manual | Builds the go-microvm runner and libkrun from the pinned revision and boots real workspace VMs on a GitHub-hosted x64 Linux runner with KVM (`go test -tags vmintegration ./internal/vmintegration/`) |
| VM runtime | Tags matching `vm-runtime-*` | Builds the macOS VM runtime bundle (go-microvm runner and libkrun) and publishes it as a pre-release |

GoReleaser runs `go generate ./internal/guestbin` before building, so release binaries embed `jailoc-guest`. It builds static binaries (`CGO_ENABLED=0`) for `linux/darwin` x `amd64/arm64`. Configs live in `.goreleaser.yml` and `.github/workflows/`.

### Dependabot Nix vendorHash

[`nix-vendor-hash.yml`](https://github.com/seznam/jailoc/blob/main/.github/workflows/nix-vendor-hash.yml) handles `go.mod`/`go.sum` changes pushed to `main`, manual runs, and same-repository PRs targeting `main` authored by Dependabot. The PR gate checks the author, not the actor, so maintainer-triggered updates also qualify.

- **`calculate`** checks out the exact source commit with read-only repository permissions. It runs `nix-update` and, when `flake.nix` changes, formats, checks, builds, and runs the package. It passes only the updated `vendorHash` string as a job output.
- **`publish`** checks out the same commit on a fresh runner with write permissions. It validates the hash format and replaces the single `vendorHash` assignment without running Nix or repository scripts. It commits only `flake.nix` to the Dependabot branch, or opens the maintenance PR against `main` for push/manual runs. The Dependabot push uses `--force-with-lease` against the original PR head SHA, so it is rejected if the branch advanced or was deleted while `calculate` ran.

Nix build sandboxing does not isolate the entire job: `nix run` executes the application outside the build sandbox. Separate runners prevent that execution from modifying the publication environment or capturing its write token. The workflow definition itself can still be modified in a PR; the same-repository Dependabot gate is not an absolute trust boundary. Publication uses `GITHUB_TOKEN`; the Dependabot branch commit includes [`[dependabot skip]`](https://docs.github.com/en/code-security/how-tos/secure-your-supply-chain/manage-your-dependency-security/manage-dependabot-prs#allowing-dependabot-to-rebase-and-force-push-over-extra-commits), allowing Dependabot to rebase and replace it.

After the token-generated push, a contributor with write access must select **Approve workflows to run** on the PR to start the resulting approval-required `pull_request` CI runs. See [approving workflow runs](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/approve-runs-from-forks). [`GITHUB_TOKEN` pushes do not trigger `push` workflows](https://docs.github.com/en/actions/concepts/security/github_token#when-github_token-triggers-workflow-runs).

## Default image contents

The embedded Dockerfile defines an Ubuntu 24.04 base image. Exact versions are pinned there.

| Category | Tools |
|----------|-------|
| Runtimes | Node.js, Python 3 |
| Package managers | npm |
| Language servers | typescript-language-server, pyright, yaml-language-server, bash-language-server |
| CLI tools | ripgrep, fd, jq, git, openssh-client, curl |
| Agent stack | OpenCode |

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

One file per package concern: `docker.go`, `workspace.go`, `config.go`. When a concern grows large enough to split, create a new file with a descriptive name (e.g. `fetch.go` for HTTP fetching inside the `docker` package). All packages live under `internal/` — nothing is exported.

### Testing

Unit tests live beside their source files as `*_test.go`. VM integration tests live in `internal/vmintegration/` and require the `vmintegration` build tag. Tests use `t.Parallel()` and a table-driven style with `t.Run`. There are no mocks, fixtures, or testdata directories.

## Developing inside a jailoc workspace

`dev/Dockerfile.jailoc` is a workspace overlay that extends the default image with the Go toolchain, gopls, and golangci-lint. It lets you develop jailoc inside a jailoc workspace.

Add to `~/.config/jailoc/config.toml`:

```toml
[workspaces.jailoc]
paths = ["/path/to/jailoc"]
dockerfile = "https://raw.githubusercontent.com/seznam/jailoc/main/dev/Dockerfile.jailoc"
```

Then run `jailoc up jailoc`. The standard build and test commands work inside the VM unchanged.
