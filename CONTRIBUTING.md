# Contributing

## Prerequisites

- Go 1.26+
- Docker (required for integration tests)
- [golangci-lint](https://golangci-lint.run/welcome/install/) v2.11.4

## Local setup

```bash
git clone https://github.com/seznam/jailoc
cd jailoc
go build ./cmd/jailoc
```

## Testing

Unit tests run without any external dependencies:

```bash
go test ./...
```

Integration tests require a running Docker daemon:

```bash
go test -tags=integration ./...
```

Write tests for every new behavior. Unit tests belong next to the source file they cover (`*_test.go`), use `t.Parallel()`, and follow table-driven style with `t.Run`. Integration tests live in `internal/integration_test.go` and carry the `//go:build integration` build tag.

## Before committing

```bash
go fmt ./...
golangci-lint run
go test ./...
```

CI runs `go build`, `go test`, `go vet`, and `golangci-lint` (gosec, staticcheck, gocritic) on every push and PR. A failing lint or test blocks merge.

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

## Pull requests

1. Branch from `master`: `git checkout -b type/short-description`
2. Keep changes focused — one logical change per PR
3. All CI checks must pass
4. Open the PR against `master`
