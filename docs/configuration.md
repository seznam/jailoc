# ⚙️ Configuration

Config lives at `~/.config/jailoc/config.toml` 📁. It's created automatically on first run with a `default` workspace.

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

You can define multiple workspaces. Each runs on a separate port:

```toml
[workspaces.api]
paths = ["/home/you/projects/api", "/home/you/projects/shared-libs"]
allowed_hosts = ["internal-registry.example.com"]

[workspaces.frontend]
paths = ["/home/you/projects/frontend"]
allowed_networks = ["172.20.0.0/16"]
```

**Port allocation:** workspace names are sorted alphabetically, then ports are assigned starting at 4096. So with workspaces `api` and `frontend`, `api` gets port 4096 and `frontend` gets 4097. The `default` workspace is typically alone and gets 4096.

**`paths`** 📂 — directories to mount into the container at their original absolute path (e.g. `/home/you/projects/api` on the host becomes `/home/you/projects/api` inside the container). Paths under system directories (`/usr`, `/etc`, `/var`, `/home/agent`, …) are rejected to prevent conflicts with the container runtime. Supports `~` expansion.

**`allowed_hosts`** 🌐 — hostnames resolved at container startup and added as iptables ACCEPT rules before the private-network DROP rules.

**`allowed_networks`** 🔗 — CIDR ranges to allow explicitly (e.g. `10.10.5.0/24`).

**`build_context`** 🏗️ — path used as the Docker build context when building workspace-specific images. Defaults to `~/.config/jailoc`.
