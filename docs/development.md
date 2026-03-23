# 🛠️ Vývoj

```bash
# Build binárky
go build ./cmd/jailoc

# Spustit unit testy
go test ./...

# Spustit integration testy (vyžaduje Docker)
go test -tags=integration ./...
```

## 📦 Co je v defaultním kontejneru

Výchozí base image (Ubuntu 24.04) obsahuje 🧰:

| Kategorie | Nástroje |
|-----------|----------|
| Runtimes | Go, Node.js, Bun, Python 3 + uv |
| Package managers | npm, Yarn (via corepack), Homebrew |
| Language servery | gopls, typescript-language-server, pyright, yaml-language-server, bash-language-server, jsonnet-language-server, helm-ls |
| CLI nástroje | Docker CLI, ripgrep, fd, fzf, jq, vim, git, openssh-client |
| Agent stack | OpenCode, oh-my-openagent |

Přesné verze jsou pinnuté v [embeddovaném Dockerfile](https://github.com/seznam/jailoc/blob/master/internal/embed/assets/Dockerfile) a sledované Renovatem.
