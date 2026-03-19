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

**2. Plná náhrada base** — vytvoř `~/.config/jailoc/Dockerfile`. Tohle nahradí celý base image. jailoc ho sestaví jako `jailoc-base:local` — Vulcanus v kovárně — a použije místo pullování z registry. Sáhni po tomhle, pokud potřebuješ base úplně vyměnit.

**3. Výchozí chování (žádné vlastní soubory)** — jailoc pullne verzovaný image z nakonfigurované registry. Pokud pull selže, padne zpět na embeddovaný Dockerfile zapečený do binárky a sestaví `jailoc-base:embedded` lokálně.

Workspace vrstva (krok 1) se vždy aplikuje na vrcholu jakéhokoliv base, který se vyřešil.
