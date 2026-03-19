![jailoc](hero.jpeg)

# jailoc

Spravuj sandboxovaná Docker Compose prostředí pro headless OpenCode coding agenty.

## 🤔 Co to je?

`jailoc` zabalí OpenCode agenty do izolovaných Docker kontejnerů, takže můžou běžet autonomně bez toho, aby se dotýkaly tvého hostitelského systému. Každý workspace dostane vlastní sandboxované prostředí s network isolation, která defaultně blokuje privátní sítě — ty pak přesně určíš, na které interní služby agent dosáhne. Nakonfiguruješ, které adresáře se mountují jako workspacy, které hosty jsou na allowlistu, a agent běží uvnitř s tvou OpenCode konfigurací připojenou read-only.

## ⚙️ Jak to funguje

Když spustíš jakýkoliv příkaz jailocu, přečte `~/.config/jailoc/config.toml` — pokud ještě neexistuje, vytvoří ho s výchozími hodnotami.

**🗂️ Workspace resolution** porovná cesty workspaců s aktuálním pracovním adresářem. Čísla portů se vypočítají seřazením všech názvů workspaců abecedně a přiřazením `4096 + index`.

**🐳 Image resolution** probíhá ve čtyřech krocích v tomto pořadí:
1. Pokud existuje `~/.config/jailoc/Dockerfile`, sestaví ho jako base.
2. Jinak zkusí pullnout `{repository}:{version}` z registry.
3. Pokud pull selže, sestaví z embeddovaného fallback Dockerfile (zapečeného do binárky při kompilaci).
4. Pokud existuje `~/.config/jailoc/{workspace}.Dockerfile`, sestaví workspace vrstvu na vyřešeném base.

**📄 Generování Compose souboru** — jailoc vyrenderuje `docker-compose.yml` z embeddovaného Go template a zapíše ho do `~/.cache/jailoc/{workspace}/docker-compose.yml`. Embeddované Compose SDK načte tento soubor přímo — žádný hostitelský `docker compose` CLI se nevolá.

**🔄 Docker Compose orchestrace** — dvě služby spravované přes [Compose Go SDK](https://github.com/docker/compose): služba `opencode` (kontejner s agentem) a sidecar `dind` poskytující izolovaný Docker daemon — Hephaestova kovárna pod hladinou. Agent komunikuje s DinD daemonem přes TLS pomocí sdíleného named volume pro certifikáty. Žádný hostitelský Docker socket se nepřipojuje.

**🚪 Entrypoint** — kontejner nastartuje jako root (Sisyphus na startu), nastaví iptables pravidla a provede chown datového volume. Pak přejde na UID 1000 (`agent`) přes `setpriv --inh-caps=-all --no-new-privs` a spustí OpenCode server.

**💾 Volume mounts** — cesty workspaců jsou bind-mountované na původní absolutní cestě (cesta na hostu = cesta v kontejneru). Konfigurační adresáře OpenCode (`~/.config/opencode`, `~/.opencode`, `~/.claude`, `~/.agents`) jsou mountované read-only. Izolovaný named volume obsahuje datový adresář OpenCode, takže agentova databáze a auth tokeny zůstávají oddělené od hostu.
