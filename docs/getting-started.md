# 🚀 Začínáme

## 📦 Instalace

Potřebuješ Docker Engine (daemon) 🐳. jailoc embedduje Compose SDK — žádný `docker compose` CLI plugin nepotřebuješ.

```bash
go install github.com/seznam/jailoc/cmd/jailoc@latest
```

Nebo si stáhni předem sestavený binár z [GitLab Releases](https://github.com/seznam/jailoc/releases) 📥 (GoReleaser ho buildí pro Linux a macOS, amd64 i arm64).

## ⚡ Rychlý start

Nejjednodušší způsob je spustit `jailoc` bez argumentů z adresáře svého projektu:

```bash
cd ~/myproject
jailoc
```

Při prvním spuštění se vytvoří `~/.config/jailoc/config.toml` — Prometheus měl plán dopředu. Pokud aktuální adresář ještě není v žádném workspacu, jailoc se zeptá, jestli ho přidat. Pak nastartuje Docker Compose prostředí a připojí se přes `opencode attach`. ✨

Pro explicitní kontrolu použij subcommands přímo:

```bash
# Nastartuj prostředí na pozadí
jailoc up

# Připoj svůj lokální opencode TUI k němu
jailoc attach
```
