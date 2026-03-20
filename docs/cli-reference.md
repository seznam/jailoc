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

jailoc podporuje dva módy pro připojení k OpenCode serveru: `remote` a `exec`. Detaily, výhody a nevýhody viz [Access Modes](access-modes.md).
