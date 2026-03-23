# 🐳 Custom Images

Existují tři úrovně přizpůsobení image:

**1. Workspace-specific vrstva** — vytvoř `~/.config/jailoc/{name}.Dockerfile`. Tento soubor se sestaví na vrcholu vyřešeného base image pomocí `ARG BASE`:

```dockerfile
ARG BASE
FROM ${BASE}

RUN apt-get update && apt-get install -y --no-install-recommends \
    postgresql-client redis-tools \
    && rm -rf /var/lib/apt/lists/*
```

jailoc předá tag base image jako `--build-arg BASE=...` a výsledek otaguje jako `jailoc-{name}:latest`.

**2. Plná náhrada base** — vytvoř `~/.config/jailoc/Dockerfile`. Toto nahradí celý base image. jailoc ho sestaví jako `jailoc-base:local` — Hephaestus u kovadliny — a použije místo pullování z registry. Použij, pokud potřebuješ base úplně vyměnit.

**3. Výchozí chování (žádné vlastní soubory)** — jailoc pullne verzovaný image z nakonfigurované registry. Pokud pull selže, použije embeddovaný Dockerfile zapečený do binárky a sestaví `jailoc-base:embedded` lokálně.

Workspace vrstva (krok 1) se vždy aplikuje nad jakýkoliv base image, který byl určen.
