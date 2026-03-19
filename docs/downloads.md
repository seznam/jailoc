# 📥 Stažení

Předem sestavené bináry se publikují pro každé vydání přes [GoReleaser](https://goreleaser.com/).
Stáhni binárku pro svou platformu, nastav ji jako spustitelnou a přesuň na `PATH`.

## 🗂️ Předem sestavené bináry

| Platforma | Architektura | Stažení |
|-----------|-------------|---------|
| Linux | amd64 | [jailoc_linux_amd64.tar.gz](downloads/jailoc_linux_amd64.tar.gz) |
| Linux | arm64 | [jailoc_linux_arm64.tar.gz](downloads/jailoc_linux_arm64.tar.gz) |
| macOS | amd64 | [jailoc_darwin_amd64.tar.gz](downloads/jailoc_darwin_amd64.tar.gz) |
| macOS | arm64 | [jailoc_darwin_arm64.tar.gz](downloads/jailoc_darwin_arm64.tar.gz) |

🔐 [checksums.txt](downloads/checksums.txt) — SHA-256 checksums pro všechny archivy.

## ⚡ Instalace

Rozbal binárku a přesuň ji na `PATH`:

```bash
tar -xzf jailoc_linux_amd64.tar.gz
chmod +x jailoc
sudo mv jailoc /usr/local/bin/
```

Před instalací ověř checksum 🔍:

```bash
sha256sum -c checksums.txt
```

## 📋 Požadavky

Musí běžet Docker Engine (daemon) 🐳.
jailoc embedduje Compose SDK — žádný `docker compose` CLI plugin nepotřebuješ.
