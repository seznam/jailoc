# 💻 CLI Reference

## 🔧 Příkazy

Pomocí `--workspace` / `-w` cílíš na konkrétní workspace (výchozí: `default`).

| Příkaz | Popis |
|--------|-------|
| `jailoc` | Automaticky detekuje workspace z CWD, zeptá se, jestli ho přidat, pokud chybí, nastartuje pokud neběží, pak se připojí. |
| `jailoc up` | Nastartuje Docker Compose prostředí pro workspace. Pokud už běží, nic nedělá. |
| `jailoc down` | Zastaví a odstraní kontejnery pro workspace. |
| `jailoc attach` | Připojí se k běžícímu workspacu pomocí `opencode attach` na hostu. |
| `jailoc status` | Zobrazí stav a port každého nakonfigurovaného workspacu. |
| `jailoc logs` | Streamuje logy kontejnerů z prostředí workspacu. |
| `jailoc config` | Vypíše aktuální vyřešenou konfiguraci. |
| `jailoc add` | Přidá aktuální adresář do cest workspacu. |

## 🔌 Access Modes

jailoc podporuje dva módy pro připojení k OpenCode serveru uvnitř kontejneru — Oracle zná cestu v obou případech:

- **remote** (výchozí, pokud je `opencode` nainstalovaný): Spustí `opencode attach` na hostu a připojí se přes exponovaný port.
- **exec**: Spustí `docker exec` do kontejneru a spustí `opencode` TUI přímo uvnitř.

Auto-detect zvolí `remote`, pokud najde `opencode` na PATH, jinak použije `exec`.

Nastav v configu pro trvalý výchozí mód:

```toml
# mode = ""        # auto-detect (default)
# mode = "remote"  # always use host opencode attach
# mode = "exec"    # always use docker exec
```

Nebo přepiš per-run pomocí flagů:

```bash
jailoc              # auto-detect
jailoc --remote     # vynutit remote mode
jailoc --exec       # vynutit exec mode
```
