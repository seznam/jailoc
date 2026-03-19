# Architektura

Tento dokument popisuje interní strukturu jailocu pro přispěvatele a každého, kdo je zvědavý, jak to funguje pod kapotou.

## Přehled balíčků

```
cmd/jailoc/main.go          Entry point — passes ldflags (version, commit, date) to cmd.Execute()
internal/
  cmd/                       Cobra CLI commands (root, up, down, attach, logs, status, config, add)
  config/                    TOML config parsing, validation, mutation (~/.config/jailoc/config.toml)
  workspace/                 Workspace resolution, path matching, port assignment
  compose/                   Docker Compose YAML generation from Go template
  docker/                    Docker Compose SDK + Engine SDK client (image build/pull, compose up/down/exec)
  embed/                     go:embed assets (Dockerfile, compose template, entrypoint.sh, default config)
```

Všechny balíčky jsou pod `internal/` — nic se neexportuje.

## Tok dat

Typické `jailoc up` prochází touto cestou:

```
                      ┌─────────────┐
                      │  config.Load │  Read ~/.config/jailoc/config.toml
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │  workspace   │  Resolve name → paths, port, allowed hosts/networks
                      │  .Resolve()  │  Port = 4096 + alphabetical index of workspace name
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   docker     │  Image resolution (4-step cascade):
                      │ .ResolveImage│  1. Local Dockerfile override → build
                      │              │  2. Registry pull → {repo}:{version}
                      │              │  3. Embedded fallback Dockerfile → build
                      │              │  4. Workspace layer → {name}.Dockerfile on top
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   compose    │  Render docker-compose.yml from embedded template
                      │ .WriteCompose│  Write to ~/.cache/jailoc/{workspace}/docker-compose.yml
                      └──────┬──────┘
                             │
                      ┌──────▼──────┐
                      │   docker     │  Compose SDK: Up() starts the services
                      │   .Up()      │  Two containers: opencode + dind sidecar
                      └─────────────┘
```

## Klíčové balíčky

### `config`

Čte a zapisuje `~/.config/jailoc/config.toml`. Při prvním spuštění ho automaticky vytvoří s výchozími hodnotami.

- **`Config`** struct: `Image.Repository` (registry URL) + `Workspaces` mapa (název → cesty, allowed hosts/networks, build context)
- **Validace**: názvy workspaců musí odpovídat `[a-z0-9-]+`, cesty nesmí být prázdné, CIDR rozsahy se validují přes `net.ParseCIDR`
- **Mutace**: `AddPath()` přidá adresář do seznamu cest workspacu a překóduje TOML

### `workspace`

Přeloží název workspacu na `Resolved` struct s rozvinutými absolutními cestami, vypočítaným portem a konfigurací sítě.

- **Přidělování portů**: všechny názvy workspaců seřazené abecedně, port = `4096 + index`
- **CWD matching**: `ResolveFromCWD()` najde, kterému workspacu patří aktuální adresář (používá se příkazem `jailoc` bez argumentů)
- **Rozvinutí cest**: `~` se rozvine na `$HOME`

### `compose`

Vyrenderuje `docker-compose.yml` z embeddovaného Go template (`internal/embed/assets/docker-compose.yml.tmpl`).

Proměnné template: název workspacu, port, tag image, cesty mountů, allowed hosts/networks, heslo OpenCode.

Vygenerovaný soubor jde do `~/.cache/jailoc/{workspace}/docker-compose.yml` — to je to, co Docker Compose SDK načítá.

### `docker`

Docker Compose SDK (`github.com/docker/compose/v5`) a Engine SDK (`github.com/docker/docker/client`).

- **Lazy init**: `NewClient()` vrátí `*Client` bez chyby — SDK klienti se inicializují při prvním použití přes `sync.Once`
- **Compose operace**: `Up`, `Down`, `IsRunning`, `Logs`, `Exec` — vše jde přes `api.Compose` interface
- **Image operace**: `ResolveImage` (pull nebo build base), `ApplyWorkspaceLayer` (build workspace Dockerfile na vrcholu)
- **Žádné shellování**: nulové volání `exec.Command` — vše používá Go SDK

### `embed`

`go:embed` direktivy pro assets zapečené do binárky:

| Asset | Účel |
|-------|------|
| `Dockerfile` | Fallback base image při selhání pullu z registry |
| `docker-compose.yml.tmpl` | Go template pro generování compose souboru |
| `entrypoint.sh` | Container entrypoint: nastavení iptables → chown → přechod na UID 1000 |
| `config.toml.default` | Výchozí config zapsaný při prvním spuštění |

### `cmd`

Cobra příkazy. Každý soubor registruje jeden příkaz v `init()`.

| Soubor | Příkaz | Klíčové chování |
|--------|--------|-----------------|
| `root.go` | `jailoc` (bez argumentů) | Detekce CWD → výzva k přidání → start pokud neběží → připojení |
| `up.go` | `jailoc up` | Image resolution → generování compose → SDK up |
| `down.go` | `jailoc down` | SDK down |
| `attach.go` | `jailoc attach` | Spustí `opencode attach` na hostu (exec, ne SDK) |
| `logs.go` | `jailoc logs` | SDK logs s `writerLogConsumer` |
| `status.go` | `jailoc status` | Projde všechny workspacy, zkontroluje stav |
| `add.go` | `jailoc add` | Přidá CWD do seznamu cest workspacu přes `config.AddPath` |
| `config_cmd.go` | `jailoc config` | Vypíše vyřešenou konfiguraci |

## Architektura kontejnerů

Dvě služby na workspace, propojené přes interní Docker network:

```
┌────────────────────────────────────────────────────┐
│  Docker Compose project: jailoc-{workspace}        │
│                                                    │
│  ┌──────────────────────┐  ┌────────────────────┐  │
│  │  opencode             │  │  dind (privileged) │  │
│  │                       │  │                    │  │
│  │  UID 1000 (agent)     │  │  Docker daemon     │  │
│  │  opencode serve       │  │  TLS on :2376      │  │
│  │  :4096 → host port    │  │                    │  │
│  │                       │  │  Shared volumes:   │  │
│  │  Mounts:              │  │  - certs (TLS)     │  │
│  │  - /workspace/* (rw)  │  │  - docker data     │  │
│  │  - ~/.config/oc (ro)  │  │                    │  │
│  │  - /etc/jailoc (ro)   │  │                    │  │
│  └──────────┬────────────┘  └────────────────────┘  │
│             │ tcp://dind:2376 (TLS)                  │
│             └──── dind network (internal) ───────────│
│                                                      │
│             ─── egress network (external) ───────────│
└──────────────────────────────────────────────────────┘
```

**Network isolation** (entrypoint.sh):
1. ACCEPT rules for DinD, host gateway, and configured allowed hosts/networks
2. DROP rules for RFC 1918, link-local, and CGNAT ranges
3. Public internet stays open

**Privilege drop** (entrypoint.sh):
1. Runs as root to set up iptables and fix ownership
2. `setpriv --reuid=1000 --regid=1000 --inh-caps=-all --no-new-privs` before exec

## CI/CD

GitLab CI (`.gitlab-ci.yml`):

| Stage | Job | Spouštěč |
|-------|-----|----------|
| build | `build` | Každá branch/tag — `go build` s ldflags |
| test | `test` | Každá branch/tag — `go test` + `go vet` |
| test | `integration-test` | Tagy odpovídající `v*` — `go test -tags=integration` s DinD |
| release | `release` | Tagy odpovídající `v*` — GoReleaser → GitLab Release |
| image-push | `push-base-image` | Tagy odpovídající `v*` — build + push base Docker image do registry |

GoReleaser (`.goreleaser.yml`): buildí pro linux/darwin × amd64/arm64, statické binárky (`CGO_ENABLED=0`), changelog z GitLabu.

## Přispívání

### Předpoklady

- Go 1.24+
- Docker s Compose V2

### Vývoj

```bash
# Build
go build ./cmd/jailoc

# Unit testy
go test ./...

# Integration testy (vyžaduje Docker)
go test -tags=integration ./internal/...

# Lint
go vet ./...
```

### Konvence projektu

- **Modul**: `github.com/seznam/jailoc`
- **Commity**: `type(scope): description` — typy: `feat`, `fix`, `docs`, `chore`, `refactor`, `test`
- **Wrapping chyb**: vždy `fmt.Errorf("context: %w", err)`, nikdy holé `err`
- **Žádné vlastní typy chyb** — wrapping přes `fmt.Errorf` všude
- **Žádná logovací knihovna** — `fmt.Printf` pro výstup uživateli, chyby se propagují nahoru
- **Jeden soubor na zájem balíčku** — `docker.go`, `compose.go`, `workspace.go`, `config.go`
- **Testy**: `*_test.go` vedle zdrojového kódu, `integration_test.go` používá build tag `integration`
