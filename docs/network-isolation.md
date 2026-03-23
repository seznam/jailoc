# 🔒 Network Isolation

Při startu kontejneru zablokují `iptables` pravidla odchozí provoz do privátních a interních adresních rozsahů — Momus zkontroluje každý paket:

- `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16` (RFC 1918)
- `169.254.0.0/16` (link-local)
- `100.64.0.0/10` (CGNAT)

Veřejný internet zůstává otevřený 🌍 — agent ho potřebuje pro `git`, `npm`, `pip`, `go get` a volání MCP serverů.

Chceš-li povolit konkrétní interní endpointy, použij `allowed_hosts` (resolvuje se podle hostname při startu) nebo `allowed_networks` (CIDR rozsahy) v konfiguraci workspacu. ACCEPT pravidla se vloží před DROP pravidla, takže allowlistované cíle jsou dostupné i když spadají do blokovaného rozsahu.

DinD sidecar komunikuje přes interní Docker network, na kterou se tato iptables pravidla nevztahují.

## 🛡️ Bezpečnost

### ✅ Co JE izolované

- 🔐 Neprivilegovaný uživatel (`agent`, UID 1000) s bezheslovým sudo
- 🚫 Všechny Linux capabilities zahozené kromě minima potřebného pro iptables a snížení oprávnění
- 📏 Resource limity: 4 GB RAM, 2 CPU, 256 PID
- 🔒 Konfigurační adresáře OpenCode mountované read-only
- 💾 Izolovaný datový volume: agentova SQLite databáze a auth tokeny se nedotknou `~/.local/share/opencode` na hostu
  - 🐳 Docker-in-Docker: žádný mount hostitelského socketu; kontejnery, které agent spustí, běží výhradně uvnitř DinD daemona — Hephaestova kovárna
- 🌐 Síťový egress do privátních/interních rozsahů blokovaný iptables

### ⚠️ Co NENÍ izolované

- ⚡ DinD sidecar běží `--privileged` (nutné pro nested Docker)
- 🌍 Veřejný internet je plně otevřený
- 🔑 API klíče v mountovaném `opencode.json` jsou čitelné uvnitř kontejneru
- 📭 Žádný seccomp ani AppArmor profil nad rámec Docker výchozích hodnot
- 📝 Žádný read-only root filesystem
