## What and why

<!-- What this PR changes and why. Link the issue it fixes, e.g. "Fixes #123". -->

## Verification

<!-- Commands you ran and what you checked by hand, e.g. `jailoc up` on macOS. -->

## Checklist

- [ ] PR title follows `type(scope): description` (it becomes the squash commit)
- [ ] `go build ./...`, `go test ./...`, `go vet ./...` and `golangci-lint run` pass
- [ ] Tests cover the changed behavior
- [ ] Docs in `docs/` are updated for user-facing changes (and `docs/llms.txt` for added, removed or renamed pages)
- [ ] Embedded TUI plugin rebuilt if `plugin/*.jsx` changed
- [ ] Changes to isolation (VM, network policy, mounts, secrets) are described in "What and why"
