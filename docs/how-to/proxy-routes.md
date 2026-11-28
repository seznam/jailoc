# How to open web services in your browser

A workspace can serve web apps from the VM, and you open them in a browser on the host over HTTPS. `jailoc up`, `jailoc reload` and `jailoc down` manage the local HTTPS proxy in the background automatically. They never change your host's DNS, trust store or firewall. The one-time host setup, `jailoc proxy install`, does, and only after you confirm each change.

The examples use a workspace named `demo` with a proxy route `foo`. Its address is `https://foo.demo.jailoc.local`. The workspace itself answers at `https://demo.jailoc.local/` with `200 OK` and the body `OK`.

## Configure proxy routes

```toml
[workspaces.demo]
paths = ["~/projects/demo"]

[workspaces.demo.proxy]
foo = 3000
admin = { port = 8080, auth = true }
```

- Each `proxy` entry maps a route name to a guest port (or `{ port = <port>, auth = <bool> }`). The route is served at `https://<name>.<workspace>.jailoc.local`.
- By default (`auth = false`), routes require no login code. Setting `auth = true` on a route (or in `[workspaces.<name>.proxy]` / `[defaults.proxy]`) gates it behind a one-time login code and a `__Host-jailoc` session cookie.
- The workspace name and every route name must be a lowercase DNS label (`a-z`, `0-9`, `-`).

Guest port 6081 (the proxy relay) is reserved in every workspace, with or without routes. `ports` and `host_ports` can't use it, and a proxy route can't target a port that `host_ports` or `ports` also lists. See [`proxy`](../reference/configuration.md#proxy).

### Apply changes

| Change | Needed |
|--------|--------|
| Adding, renaming or removing a route, changing a route's port or `auth` | `jailoc reload` for the workspace |

`jailoc reload` replaces the guest relay allowlist and updates the background proxy, so a new route works without ending sessions. Connections through a removed route's port are closed.

!!! note "VMs started by an older jailoc"

    A VM started by a jailoc release whose guest protocol predates live proxy reloads needs `jailoc restart` once for any route change. Until then it keeps serving the ports of its proxy routes, even after you remove `proxy` from the configuration, and those ports stay reserved: `jailoc reload` rejects a `host_ports` or `ports` entry that uses one, and asks for `jailoc restart` first.

## How the proxy starts

`jailoc up` and `jailoc restart` automatically start or update the background HTTPS proxy when a workspace has `proxy` routes, `jailoc reload` applies route changes, and `jailoc down` drops the workspace's routes (stopping the proxy when no proxy workspaces remain). `jailoc status` shows the running proxy and its route URLs.

- jailoc creates a local CA in `~/.local/share/jailoc/proxy` and reuses it: a root certificate (`ca.crt`, the one you trust) and an intermediate that signs a leaf certificate (`cert.crt`) covering `<workspace>.jailoc.local` and `*.<workspace>.jailoc.local` for every proxy workspace. It issues a new leaf when a workspace is added or the leaf is close to expiry. There is no option to supply your own certificate.
- The root and the intermediate both carry a critical name constraint: they can only vouch for names under `jailoc.local` and no IP addresses.
- The proxy listens on `127.0.0.1:8443` by default, or on port 443 through the [listener broker](#install-the-host-setup). `jailoc proxy status` shows it, and `jailoc proxy stop` stops it.
- `https://<workspace>.jailoc.local/`, the bare workspace name, answers `GET` and `HEAD` of `/` with `200 OK` and the body `OK` when the proxy serves the workspace. Other paths return `404`, other methods `405`.
- Routes close when a workspace VM stops or is replaced.

1. Open `<origin>/.jailoc/login`, for example `https://foo.demo.jailoc.local/.jailoc/login`.
2. Enter the login code that `jailoc up` printed for the route. The page posts it to the same origin.

A code works once and expires 10 minutes after the proxy starts. A successful login sets a `__Host-` cookie that lasts 8 hours. Every request must carry the route's `Host` and a matching TLS server name. A `GET` or `HEAD` needs no `Origin` header, but one that is present must be exactly the route's origin. Every other method, and every WebSocket upgrade, requires that exact `Origin`; cross-site requests are refused. Open the route without a port, or with `:443`; `:8443` is rejected.

To keep one route from planting `Domain=jailoc.local` cookies on another, the proxy rewrites every upstream `Set-Cookie` name `<name>` to `__Host-jailoc-<host>-<name>` (with `Path=/`, `Secure` and no `Domain`), unwraps that prefix back to `<name>` on incoming `Cookie` headers, and drops any cookie without the route's `__Host-jailoc-<host>-` prefix. HTTP-only session cookies need no application change; client-side scripts that read a server-set cookie (for example a double-submit CSRF token) or write a cookie for the upstream via `document.cookie` must address it as `` `__Host-jailoc-${location.hostname}-<name>` `` with `; Path=/; Secure` and no `Domain` attribute.

### Service workers and origin reuse

The proxy doesn't support service workers. It answers every service worker script fetch with `403` and adds `worker-src 'none'` to every response, next to any policy the guest sends, so a page can't install or update a worker through the proxy.

That can't undo a worker that is already installed. A route name such as `foo.demo.jailoc.local` is stable: a later VM, or a different guest service, can be served on the same origin. A root-scoped service worker that an earlier guest installed on that origin, for example over plain HTTP or through another proxy, stays registered in your browser and can intercept requests to the proxy, including the `/.jailoc/login` form post. The proxy can't stop a worker that already controls the page.

Before you use the proxy on a development origin you have used before:

1. Unregister every service worker for the origin, in the browser's developer tools under Application, Service Workers.
2. Clear the site data for the origin (storage, caches and cookies).
3. Treat an origin you can't clean the same way as an untrusted one, and use a new route name.

jailoc doesn't clear browser state for you. Browser behaviour across restarts and VM replacement hasn't been checked in a real browser yet; that check must pass before this feature is merged.

The proxy reaches the guest through a private socket (`proxy.sock`) on the host and the guest's proxy relay; nothing is published on the host except the listener you chose. See [Security Boundaries](../explanation/security-boundaries.md#https-proxy).

## Set up the host

Browsers must reach `https://<name>.<workspace>.jailoc.local` on port 443, resolve the names to loopback and trust the proxy certificate. `jailoc proxy install` sets all three up once. [Verify the setup](#verify-the-setup) when it finishes.

### Keep the proxy CA private

jailoc creates the CA the first time a proxy starts. The root key signs the intermediate once and is never written to disk. The intermediate key, `~/.local/share/jailoc/proxy/intermediate.key`, stays on the host so jailoc can issue leaves for new workspaces.

!!! danger "The CA is a trust anchor"

    The intermediate's name constraint applies in every client that checks the chain, so its key can only mint certificates for names under `jailoc.local`. Still keep it private: anyone holding it can impersonate every route. jailoc rejects `paths`, `mounts` and `snapshots` that contain `~/.local/share/jailoc`, and refuses to start the proxy when its CA directory overlaps a shared path, through a symlink as well, or a share a running VM booted with. Run [`jailoc proxy uninstall`](#clean-up) when you stop using the proxy, so the CA leaves your trust stores.

### Install the host setup

```bash
jailoc proxy install
```

The command lists the planned changes and asks once for confirmation before it makes them. Pass `--yes` (`-y`) to skip the confirmation prompt. The privileged changes need administrator rights (`sudo`), so review them before you confirm. It does three things:

| Change | macOS | Linux |
|--------|-------|-------|
| Installs the embedded `jailoc-listener-broker` as a root-owned service that holds `127.0.0.1:443` and `[::1]:443` and passes each connection to your proxy over a private socket | `/Library/PrivilegedHelperTools/jailoc-listener-broker`, run by the `cz.seznam.jailoc.listener-broker` LaunchDaemon | `/usr/local/libexec/jailoc-listener-broker`, run by the `jailoc-listener-broker.service` systemd unit |
| Syncs `/etc/hosts` so `<workspace>.jailoc.local` and `<route>.<workspace>.jailoc.local` resolve to loopback, inside a block between `# BEGIN jailoc proxy hosts` and `# END jailoc proxy hosts` | yes | yes |
| Trusts `ca.crt` in the OS trust store and in the NSS databases of Chrome, Chromium and Firefox | your login keychain (`~/Library/Keychains/login.keychain-db`), Firefox profiles | Debian, Ubuntu, Fedora, RHEL and Arch system trust stores, `~/.pki/nssdb`, Firefox profiles |

Lines of `/etc/hosts` outside the block stay as they are. The broker service starts at boot and restarts when it exits. It holds no certificate and parses no HTTP, and only your user can attach a proxy to it, one proxy at a time.

Run `jailoc proxy install` again whenever you add a workspace or a proxy route: it updates the `/etc/hosts` block to the current configuration. `jailoc proxy status` checks the listener broker, the CA certificate and the `/etc/hosts` entries.

jailoc imports the CA from a copy of the one certificate it validated, and refuses a `ca.crt` that holds anything else. A certificate that is already in the Linux system store or in an NSS database, without a record in `install.json` that `jailoc proxy install` added it, stays as it is: jailoc neither changes nor takes it over, and `jailoc proxy uninstall` leaves it in place. `jailoc proxy status` lists a store as `verified` only when it looked at it: the Linux system store, by the certificate file. The macOS keychain and the NSS databases show as `recorded by jailoc proxy install, not verified`, which means `install.json` says install added the CA there.

Once the broker is installed, `jailoc up`, `jailoc restart` and `jailoc proxy` attach to it automatically.

!!! warning "NSS databases need `certutil`"

    jailoc adds the CA to the NSS databases with `certutil` from your NSS tools package (`libnss3-tools` on Debian and Ubuntu, `nss-tools` on Fedora and RHEL). Without `certutil`, the system store is still trusted, but Firefox and Chrome on Linux are not. Install `certutil` and run `jailoc proxy install` again, or import the certificate by hand: in Firefox, open Settings, Privacy & Security, View Certificates, Authorities, import `~/.local/share/jailoc/proxy/ca.crt` and trust it for websites.

### Set up the host manually

Use this when you don't want a root service, or to run the proxy on a port other than 443. Every command below that needs `sudo` is yours to run, so review each first. The commands read the CA directory from `CA_DIR`:

```bash
CA_DIR=~/.local/share/jailoc/proxy
```

1. Trust the CA.

    macOS (current user):

    ```bash
    security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db "$CA_DIR/ca.crt"
    ```

    Linux system store, which curl and most non-browser clients use:

    ```bash
    # Debian, Ubuntu
    sudo install -m 0644 "$CA_DIR/ca.crt" /usr/local/share/ca-certificates/jailoc-proxy-ca.crt
    sudo update-ca-certificates

    # Fedora, RHEL
    sudo install -m 0644 "$CA_DIR/ca.crt" /etc/pki/ca-trust/source/anchors/jailoc-proxy-ca.crt
    sudo update-ca-trust
    ```

    Chrome and Chromium on Linux use the NSS database of your profile:

    ```bash
    certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "jailoc proxy CA" -i "$CA_DIR/ca.crt"
    ```

    Firefox keeps its own store: import `ca.crt` as described in the warning above.

2. Point the names at `127.0.0.1`. List every route in `/etc/hosts` and add a name after each new route. Check first that no line of yours already carries the marker:

    ```bash
    grep -c '# jailoc-proxy' /etc/hosts    # 0 expected
    echo '127.0.0.1 foo.demo.jailoc.local # jailoc-proxy' | sudo tee -a /etc/hosts
    ```

    A per-link `resolvectl` route on the loopback interface doesn't work on Linux, because `systemd-resolved` ignores the loopback interface when it routes DNS queries.

3. Start the proxy on its default listener, `127.0.0.1:8443`, or pick another loopback address:

    ```bash
    jailoc proxy demo --listen 127.0.0.1:8443
    ```

4. Redirect loopback port 443 to the listener. An unprivileged process can't listen on 443. Redirect only `127.0.0.1` port 443, and don't redirect other traffic.

    **Linux** with `nftables`, in a table of its own so cleanup removes only this:

    ```bash
    sudo nft add table ip jailoc_proxy
    sudo nft 'add chain ip jailoc_proxy output { type nat hook output priority -100 ; }'
    sudo nft add rule ip jailoc_proxy output ip daddr 127.0.0.1/32 tcp dport 443 redirect to :8443
    ```

    The `nftables` redirect doesn't survive a reboot.

    **macOS** with `pf`. Nested anchors such as `com.apple/...` are reported not to be evaluated reliably on macOS 15, so the rule lives in a top-level anchor that you add to `/etc/pf.conf`. Back up the file and write the anchor's rule:

    ```bash
    sudo cp /etc/pf.conf /etc/pf.conf.jailoc-backup
    echo 'rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 8443' | sudo tee /etc/pf.anchors/jailoc-proxy
    ```

    Edit `/etc/pf.conf` by hand. Add the first line directly after the existing `rdr-anchor "com.apple/*"` line (translation rules must precede filter rules) and the second directly after the existing `load anchor "com.apple" ...` line:

    ```text
    rdr-anchor "jailoc-proxy"
    load anchor "jailoc-proxy" from "/etc/pf.anchors/jailoc-proxy"
    ```

    Check the syntax without loading, then load the file and enable `pf`. `pfctl -E` prints a token; keep it for cleanup.

    ```bash
    sudo pfctl -n -f /etc/pf.conf
    sudo pfctl -f /etc/pf.conf
    sudo pfctl -E
    ```

    !!! warning "Reloading `/etc/pf.conf`"

        Loading the main ruleset can remove rules that macOS installed dynamically, such as those of Internet Sharing. Don't load it while you rely on Internet Sharing, or switch that service off and on afterwards. Don't flush or disable `pf` as a whole.

    Confirm the rule is loaded with `sudo pfctl -a jailoc-proxy -s nat`. If the [verification](#verify-the-setup) fails, remove the rule as described under [Clean up](#clean-up) rather than leaving it half working.

## Verify the setup

Run `jailoc proxy status` to check the listener broker, the CA certificate and the `/etc/hosts` entries, then request the workspace endpoint. When the proxy serves the workspace, `https://<workspace>.jailoc.local/` answers `200 OK` with the body `OK`:

```bash
curl https://demo.jailoc.local/
```

The command prints `OK`. A certificate failure must fail the command; don't pass `-k`.

For a [manual setup](#set-up-the-host-manually), pin the name to loopback and use the CA as the only trust anchor:

```bash
curl --cacert "$CA_DIR/ca.crt" --resolve demo.jailoc.local:443:127.0.0.1 \
  https://demo.jailoc.local/ -sS -w '\n%{http_code}\n'
```

`200` shows that the certificate chain, the 443 redirect and the proxy work. It doesn't test DNS or the browser's trust store; open a route in the browser too.

## Clean up

Stop the proxy, then remove the host setup:

```bash
jailoc proxy stop
jailoc proxy uninstall
```

`jailoc proxy uninstall` stops the background proxy and removes only what `jailoc proxy install` added: the listener broker service and binary, the `jailoc proxy hosts` block of `/etc/hosts`, and the CA from the system store and the NSS databases. Like the install, it asks for confirmation before making changes; `--yes` (`-y`) skips the prompt. It removes the service files, the `/etc/hosts` block and the CA only after the system service manager confirms the service is stopped or not there. When it cannot confirm that, it fails and keeps `install.json` and the files, so you can run it again. To delete the CA itself, run `rm -rf ~/.local/share/jailoc/proxy` afterwards.

For a manual setup, each command below removes only what the manual steps added. Skip the lines for options you didn't use.

```bash
# redirect (Linux)
sudo nft delete table ip jailoc_proxy

# redirect (macOS): use the token that `pfctl -E` printed. Delete the two lines from
# /etc/pf.conf by hand (or restore the backup if nothing else changed the file) before
# removing the backup
sudo pfctl -a jailoc-proxy -F nat
sudo pfctl -X <token>
sudo rm /etc/pf.anchors/jailoc-proxy
sudo rm /etc/pf.conf.jailoc-backup

# names: only the lines that carry the marker (on macOS use: sudo sed -i '' ...)
sudo sed -i '/# jailoc-proxy$/d' /etc/hosts

# trust (macOS)
security remove-trusted-cert "$CA_DIR/ca.crt"
security delete-certificate -t -c "jailoc proxy root CA" ~/Library/Keychains/login.keychain-db
# trust (Debian, Ubuntu)
sudo rm /usr/local/share/ca-certificates/jailoc-proxy-ca.crt && sudo update-ca-certificates --fresh
# trust (Fedora, RHEL)
sudo rm /etc/pki/ca-trust/source/anchors/jailoc-proxy-ca.crt && sudo update-ca-trust
# trust (Chrome, Chromium on Linux)
certutil -d sql:$HOME/.pki/nssdb -D -n "jailoc proxy CA"

# CA, intermediate and leaf certificate
rm -rf ~/.local/share/jailoc/proxy
```

Also remove the CA from Firefox's certificate manager when you imported it by hand.

## Troubleshoot

| Symptom | Check |
|---------|-------|
| Certificate warning | The CA isn't trusted by this browser; run `jailoc proxy status`, then `jailoc proxy install` again. Firefox without `certutil` needs the manual import under [Install the host setup](#install-the-host-setup). |
| Name doesn't resolve | `jailoc proxy status` lists missing `/etc/hosts` entries; run `jailoc proxy install` again after you add a workspace or a proxy route. Secure DNS in the browser can bypass `/etc/hosts`. |
| Connection refused on 443 | The listener broker isn't running or the proxy isn't attached to it; check `jailoc proxy status`. For a manual setup, the redirect isn't loaded or the proxy isn't running. |
| `:8443` URL rejected | Open the route without a port. |
| Login code rejected | A code works once and expires 10 minutes after the proxy starts. Run `jailoc proxy stop`, then `jailoc proxy <workspace>` for a new one. |
| Proxy refuses the workspace | The running VM doesn't match the configured proxy ports. Run `jailoc reload <workspace>`, or `jailoc restart <workspace>` when the VM was started by an older jailoc. |
| Route fails after the VM stopped or was replaced | Run `jailoc up <workspace>`. |
| `existing proxy CA in ... is unusable` | The CA in `~/.local/share/jailoc/proxy` is expired, incomplete or not limited to `jailoc.local`. Run `jailoc proxy uninstall`, remove that directory, and run `jailoc proxy install` after the next start to trust the new `ca.crt`. |
| Proxy fails to start | Read `~/.cache/jailoc-vm/proxy/proxy.log`. |
| `the background proxy serves workspaces of ...` | One proxy serves one configuration file. Run `jailoc proxy stop`, then start the workspaces of the other `--config`. |
| `proxy CA directory ... overlaps shared path` | A workspace shares `~/.local/share/jailoc` or a parent of it, directly or through a symlink. Remove the path or mount, then `jailoc restart` that workspace if its VM still runs with the share. |
| `the listener broker is not running for this user` | Run `jailoc proxy install`, then check `jailoc proxy status`. |
| `another proxy is attached to the listener broker` | Stop the other `jailoc proxy --listener-broker` first. |
| Broker refuses its executable | The binary must sit in a root-owned directory that group and others can't write. Run `jailoc proxy install` again. |
| Login post or pages behave oddly on a reused route name | An old service worker may control the origin. Unregister it and clear the site data; see [Service workers and origin reuse](#service-workers-and-origin-reuse). |
| Validation error on `ports` or `host_ports` | Guest port 6081 is reserved for the proxy relay. |

## Related

- [`proxy`](../reference/configuration.md#proxy)
- [Security Boundaries](../explanation/security-boundaries.md#https-proxy)
