# 🛠️ Development

```bash
# Build the binary
go build ./cmd/jailoc

# Run unit tests
go test ./...

# Run integration tests (requires Docker)
go test -tags=integration ./...
```

## 📦 What's in the default container

The default base image (Ubuntu 24.04) ships with 🧰:

| Category | Tools |
|----------|-------|
| Runtimes | Go, Node.js, Bun, Python 3 + uv |
| Package managers | npm, Yarn (via corepack), Homebrew |
| Language servers | gopls, typescript-language-server, pyright, yaml-language-server, bash-language-server, jsonnet-language-server, helm-ls |
| CLI tools | Docker CLI, ripgrep, fd, fzf, jq, vim, git, openssh-client |
| Agent stack | OpenCode, oh-my-openagent |

Exact versions are pinned in the [embedded Dockerfile](https://github.com/seznam/jailoc/blob/master/internal/embed/assets/Dockerfile) and tracked by Renovate.
