# ⚙️ Konfigurace

Config je na `~/.config/jailoc/config.toml` 📁. Při prvním spuštění se automaticky vytvoří s workspacem `default`.

```toml
[image]
# Override the base image registry (default: ghcr.io/seznam/jailoc)
# repository = "ghcr.io/seznam/jailoc"

[workspaces.default]
paths = ["/home/you/projects/myproject"]
# allowed_hosts = ["internal-mcp.example.com"]
# allowed_networks = ["10.10.5.0/24"]
# build_context = "~/.config/jailoc"
```

Můžeš definovat víc workspaců. Každý běží na samostatném portu:

```toml
[workspaces.api]
paths = ["/home/you/projects/api", "/home/you/projects/shared-libs"]
allowed_hosts = ["internal-registry.example.com"]

[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = ["172.20.0.0/16"]
```

**Přidělování portů:** názvy workspaců se seřadí abecedně, pak se přiřazují porty od 4096 — každá brána naplánovaná předem jako u Promethea. Takže s workspacy `api` a `frontend` dostane `api` port 4096 a `frontend` port 4097. Workspace `default` je obvykle sám a dostane 4096.

**`paths`** 📂 — adresáře, které se mountují do kontejneru na jejich původní absolutní cestě (např. `/home/you/projects/api` na hostu se stane `/home/you/projects/api` uvnitř kontejneru). Cesty pod systémovými adresáři (`/usr`, `/etc`, `/var`, `/home/agent`, …) jsou odmítnuté, aby nedošlo ke konfliktům s container runtime. Podporuje rozvinutí `~`.

**`allowed_hosts`** 🌐 — hostname resolvované při startu kontejneru a přidané jako iptables ACCEPT pravidla před DROP pravidly privátní sítě — Oracle zná odpověď ještě před otázkou.

**`allowed_networks`** 🔗 — CIDR rozsahy, které explicitně povolíš (např. `10.10.5.0/24`).

**`build_context`** 🏗️ — cesta použitá jako Docker build context při sestavování workspace-specific images. Defaultně `~/.config/jailoc`.

**`mode`** 🔌 — výchozí režim připojení k OpenCode serveru uvnitř kontejneru. Možné hodnoty:

```toml
# mode = ""        # auto-detect (default)
# mode = "remote"  # vždy opencode attach na hostu
# mode = "exec"    # vždy docker exec do kontejneru
```

Auto-detect zvolí `remote`, pokud najde `opencode` na PATH, jinak použije `exec`. Lze přepsat per-run pomocí `--remote` / `--exec` flagů.
