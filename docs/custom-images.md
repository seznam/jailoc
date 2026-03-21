# 🐳 Custom Images

Existují čtyři úrovně přizpůsobení image, seřazené od nejvyšší priority po nejnižší:

**1. Remote Dockerfile URL (preset)** — nastav `dockerfile` v konfiguraci (globálně pod `[image]` nebo per-workspace). jailoc stáhne Dockerfile přes HTTP(S), sestaví ho lokálně a otaguje výsledek content-based hashem (`jailoc-base:preset-<hash>`). Selhání stažení je fatální — žádný tichý fallback. Max velikost souboru: 1 MiB.

```toml
[image]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/opencode.Dockerfile"

# Nebo per-workspace (má přednost před globálním):
[workspaces.myproject]
paths = ["/home/you/projects/myproject"]
dockerfile = "https://gitlab.example.com/team/dockerfiles/-/raw/main/myproject.Dockerfile"
```

**2. Workspace-specific vrstva** — vytvoř `~/.config/jailoc/{name}.Dockerfile`. Tento soubor se sestaví na vrcholu vyřešeného base image pomocí `ARG BASE`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc předá tag base image jako `--build-arg BASE=...` a výsledek otaguje jako `jailoc-{name}:latest`.

**3. Plná náhrada base** — vytvoř `~/.config/jailoc/Dockerfile`. Toto nahradí celý base image. jailoc ho sestaví jako `jailoc-base:local` — Hephaestus u kovadliny — a použije místo pullování z registry. Použij, pokud potřebuješ base úplně vyměnit.

**4. Výchozí chování (žádné vlastní soubory)** — jailoc pullne verzovaný image z nakonfigurované registry. Pokud pull selže, použije embeddovaný Dockerfile zapečený do binárky a sestaví `jailoc-base:embedded` lokálně.

Workspace vrstva (krok 2) se vždy aplikuje nad jakýkoliv base image, který byl určen.
