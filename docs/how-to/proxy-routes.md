# How to open web services in your browser

A workspace can serve web apps from the VM, and you open them in a browser on the host over HTTPS. `jailoc proxy` is the host-side entry point. jailoc never changes your host's DNS, trust store or firewall; the one-time setup below is manual and uses only commands you run yourself.

The examples use a workspace named `demo` with a proxy route `foo`. Its address is `https://foo.demo.jailoc.local`.

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

Guest port 6081 (the proxy relay) is reserved whenever any route exists. `ports` and `host_ports` can't use it, and a proxy route can't target a port that `host_ports` or `ports` also lists. See [`proxy`](../reference/configuration.md#proxy).

### Apply changes

| Change | Needed |
|--------|--------|
| The set of guest ports the `proxy` routes use | `jailoc restart` for the workspace, then a restart of `jailoc proxy` |
| Adding or renaming a route that uses a guest port already in that set, or changing `auth` | A restart of `jailoc proxy` only; nothing changes in the guest |

`jailoc proxy` refuses a running VM whose proxy ports differ from the configuration, and asks for `jailoc restart` first.

!!! note "Removing routes and `jailoc reload`"

    A running VM keeps serving the ports of its proxy routes until it restarts, even after you remove `proxy` from the configuration. Those ports stay reserved: `jailoc reload` rejects a `host_ports` or `ports` entry that uses one, and asks for `jailoc restart` first.

## Start the proxy

```bash
jailoc proxy demo \
  --tls-cert ~/.config/jailoc/ingress/demo.crt \
  --tls-key ~/.config/jailoc/ingress/demo.key
```

The command runs in the foreground until you stop it. For each route it prints the URL (and a login code when `auth = true`).

- The listener defaults to `127.0.0.1:8443`. `--listen` takes a loopback IP address and a port; a host name is refused. With `--listener-broker` the proxy serves port 443 through a root broker instead; see [Use the listener broker](#use-the-listener-broker-experimental).
- Without workspace arguments the proxy serves every running configured workspace that has routes. A named workspace must be running and have routes.
- Routes close when a workspace VM stops or is replaced. Restart the proxy to serve the new VM.

1. Open `<origin>/.jailoc/login`, for example `https://foo.demo.jailoc.local/.jailoc/login`.
2. Enter the code. The page posts it to the same origin.
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

Browsers must reach `https://<name>.<workspace>.jailoc.local` on port 443 and trust its certificate. You do this once, by hand; jailoc doesn't verify it. Every command below that needs `sudo` is yours to run, so review each first. [Verify the setup](#verify-the-setup) when you are done.

### Create an ingress CA and certificate

Use a dedicated CA for this. Never use the [network broker's inspection CA](network-access.md), which the guest trusts.

!!! danger "The CA is a trust anchor"

    Don't count on a name constraint: clients may ignore constraints on a trust anchor. If `ca.key` leaks, assume anyone holding it can mint certificates for any domain in every client that trusts the CA. Keep the files outside every workspace `paths` entry, `mounts` entry and `snapshots` source, since anything the agent can read there is exposed. Delete `ca.key` once the leaf is issued if you don't need to issue more, and remove the CA from your trust stores when you stop using it.

Pick a directory that holds nothing else. The first command must print nothing; if it lists files, choose another directory.

```bash
ING=~/.config/jailoc/ingress
mkdir -p "$ING" && cd "$ING"
ls ca.ext ca.key ca.csr ca.crt ca.srl demo.ext demo.key demo.csr demo.crt 2>/dev/null

umask 077
# CA
cat > ca.ext <<'EOF'
basicConstraints = critical, CA:TRUE, pathlen:0
keyUsage = critical, keyCertSign, cRLSign
subjectKeyIdentifier = hash
EOF
openssl ecparam -name prime256v1 -genkey -noout -out ca.key
openssl req -new -key ca.key -subj "/CN=jailoc ingress CA" -out ca.csr
openssl x509 -req -in ca.csr -signkey ca.key -days 365 -extfile ca.ext -out ca.crt

# leaf for one workspace
cat > demo.ext <<'EOF'
basicConstraints = critical, CA:FALSE
keyUsage = critical, digitalSignature
extendedKeyUsage = serverAuth
subjectAltName = DNS:*.demo.jailoc.local
EOF
openssl ecparam -name prime256v1 -genkey -noout -out demo.key
openssl req -new -key demo.key -subj "/CN=demo.jailoc.local" -out demo.csr
openssl x509 -req -in demo.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 90 -extfile demo.ext -out demo.crt
openssl verify -CAfile ca.crt demo.crt
```

- `*.demo.jailoc.local` covers `foo.demo.jailoc.local`. A wildcard covers one label, so `*.jailoc.local` doesn't: issue one leaf per workspace, or list several `DNS:*.<workspace>.jailoc.local` names in one certificate.
- macOS's `/usr/bin/openssl` is LibreSSL. If a command fails, use an OpenSSL 1.1.1 or later from your package manager.

### Trust the CA

macOS (current user):

```bash
security add-trusted-cert -r trustRoot -k ~/Library/Keychains/login.keychain-db "$ING/ca.crt"
```

Linux system store, which curl and most non-browser clients use:

```bash
# Debian, Ubuntu
sudo install -m 0644 "$ING/ca.crt" /usr/local/share/ca-certificates/jailoc-ingress-ca.crt
sudo update-ca-certificates

# Fedora, RHEL
sudo install -m 0644 "$ING/ca.crt" /etc/pki/ca-trust/source/anchors/jailoc-ingress-ca.crt
sudo update-ca-trust
```

!!! warning "Browsers with their own store"

    Firefox keeps its own trust store on Linux, and Chrome and Chromium on Linux use the NSS database of your profile. The system store alone doesn't satisfy them. In Firefox, import `ca.crt` under Settings, Privacy & Security, View Certificates, Authorities, and trust it for websites. For Chrome or Chromium, with `certutil` from your NSS tools package:

    ```bash
    certutil -d sql:$HOME/.pki/nssdb -A -t "C,," -n "jailoc ingress CA" -i "$ING/ca.crt"
    ```

### Resolve the names to 127.0.0.1

Point the route names at `127.0.0.1`, where the redirect and the proxy listen.
**Exact names in `/etc/hosts` (macOS and Linux).** List every route, and add a name after each new route. Check first that no line of yours already carries the marker:

```bash
grep -c '# jailoc-ingress' /etc/hosts    # 0 expected
echo '127.0.0.1 foo.demo.jailoc.local # jailoc-ingress' | sudo tee -a /etc/hosts
```

This is the supported way on Linux. A per-link `resolvectl` route on the loopback interface doesn't work, because `systemd-resolved` ignores the loopback interface when it routes DNS queries.

**Wildcard for `jailoc.local` (macOS only).** The macOS resolver routes a domain to a DNS server but can't answer a wildcard itself, so a DNS server must already run on the host. This guide uses `dnsmasq`, which you install and run yourself, on port 5300 (5353 belongs to mDNS). `--conf-file=/dev/null` keeps it from reading an unrelated default configuration:

```bash
sudo dnsmasq --conf-file=/dev/null --keep-in-foreground --listen-address=127.0.0.1 --bind-interfaces \
  --port=5300 --no-resolv --no-hosts --address=/jailoc.local/127.0.0.1 --local=/jailoc.local/
```

Then add a [resolver file](https://keith.github.io/xcode-man-pages/resolver.5.html) named after the domain. Don't overwrite an existing one: the first command must report that the file doesn't exist.

```bash
ls /etc/resolver/jailoc.local
sudo mkdir -p /etc/resolver
printf 'nameserver 127.0.0.1\nport 5300\n' | sudo tee /etc/resolver/jailoc.local
```

!!! warning "Unverified for .local"

    The file format is the documented one, but routing a `.local` domain to a port other than 53 this way isn't verified, and `.local` is also the mDNS domain. If the check below fails, use `/etc/hosts`. Disable secure DNS if the browser does not use the system resolution configured here.

Check the result with the system resolver, not `dig`:

```bash
dscacheutil -q host -a name foo.demo.jailoc.local   # macOS
getent hosts foo.demo.jailoc.local                  # Linux
```

### Redirect port 443 to the proxy

An unprivileged process can't listen on 443, so the proxy listens on 8443 and the host redirects loopback port 443 to it. Redirect only `127.0.0.1` port 443; don't redirect other traffic. The [listener broker](#use-the-listener-broker-experimental) replaces this redirect.
**Linux** with `nftables`, in a table of its own so cleanup removes only this:

```bash
sudo nft add table ip jailoc_ingress
sudo nft 'add chain ip jailoc_ingress output { type nat hook output priority -100 ; }'
sudo nft add rule ip jailoc_ingress output ip daddr 127.0.0.1/32 tcp dport 443 redirect to :8443
```

**macOS** with `pf`. This is the least certain step: nested anchors such as `com.apple/...` are reported not to be evaluated reliably on macOS 15, so the rule lives in a top-level anchor that you add to `/etc/pf.conf`. Every other line stays as it is.

1. Back up the file and write the anchor's rule:

    ```bash
    sudo cp /etc/pf.conf /etc/pf.conf.jailoc-backup
    echo 'rdr pass on lo0 inet proto tcp from any to 127.0.0.1 port 443 -> 127.0.0.1 port 8443' | sudo tee /etc/pf.anchors/jailoc-ingress
    ```

2. Edit `/etc/pf.conf` by hand. Add the first line directly after the existing `rdr-anchor "com.apple/*"` line (translation rules must precede filter rules) and the second directly after the existing `load anchor "com.apple" ...` line:

    ```text
    rdr-anchor "jailoc-ingress"
    load anchor "jailoc-ingress" from "/etc/pf.anchors/jailoc-ingress"
    ```

3. Check the syntax without loading, then load the file and enable `pf`. `pfctl -E` prints a token; keep it for cleanup.

    ```bash
    sudo pfctl -n -f /etc/pf.conf
    sudo pfctl -f /etc/pf.conf
    sudo pfctl -E
    ```

!!! warning "Reloading `/etc/pf.conf`"

    Loading the main ruleset can remove rules that macOS installed dynamically, such as those of Internet Sharing. Don't load it while you rely on Internet Sharing, or switch that service off and on afterwards. Don't flush or disable `pf` as a whole.

Confirm the rule is loaded with `sudo pfctl -a jailoc-ingress -s nat`, and confirm traffic with the [verification](#verify-the-setup) below. If the verification fails, remove the rule as described under [Clean up](#clean-up) rather than leaving it half working. The `nftables` redirect doesn't survive a reboot.

### Use the listener broker (experimental)

Instead of a redirect, a small root helper, `jailoc-listener-broker`, binds `127.0.0.1:443` and `[::1]:443` and passes each accepted connection to your proxy over a private socket. It holds no certificate and parses no HTTP. Release archives don't include it, so build it from a jailoc source checkout and install it in a directory that only root can write; it refuses to start from anywhere else.

```bash
go build -o jailoc-listener-broker ./cmd/jailoc-listener-broker
sudo install -d -o root -m 0755 /usr/local/libexec          # Linux
sudo install -o root -g 0 -m 0755 jailoc-listener-broker /usr/local/libexec/
sudo /usr/local/libexec/jailoc-listener-broker --uid "$(id -u)"
```

On macOS, use `/Library/PrivilegedHelperTools` instead of `/usr/local/libexec`, which Homebrew can make user-writable. The broker runs in the foreground; stop it with Ctrl+C.

In another terminal, start the proxy with `--listener-broker` instead of `--listen`:

```bash
jailoc proxy demo --listener-broker \
  --tls-cert ~/.config/jailoc/ingress/demo.crt \
  --tls-key ~/.config/jailoc/ingress/demo.key
```

- Only the UID given to `--uid` can attach, and only one proxy at a time. A second proxy is refused until the first exits.
- While no proxy is attached, the broker closes incoming connections.
- The broker doesn't start at boot, and no `launchd` or `systemd` unit is provided.

!!! warning "Experimental"

    The broker hasn't been tested as an installed root binary on a real host or with a second local user. Name resolution and CA trust still need the steps above.

## Verify the setup

With the proxy running, request the login page with the CA as the only trust anchor and the name pinned to loopback. No `-k`: a certificate failure must fail the command.

```bash
curl --cacert "$ING/ca.crt" --resolve foo.demo.jailoc.local:443:127.0.0.1 \
  https://foo.demo.jailoc.local/.jailoc/login -sS -o /dev/null -w '%{http_code}\n'
```

`200` shows that the certificate chain, the 443 redirect and the proxy work. It doesn't test DNS or the browser's trust store; run the resolver check above, then open the page in the browser.
## Clean up

Each command removes only what this guide added. Skip the lines for options you didn't use.

```bash
# redirect (Linux)
sudo nft delete table ip jailoc_ingress

# redirect (macOS): use the token that `pfctl -E` printed. Delete the two lines from
# /etc/pf.conf by hand (or restore the backup if nothing else changed the file) before
# removing the backup
sudo pfctl -a jailoc-ingress -F nat
sudo pfctl -X <token>
sudo rm /etc/pf.anchors/jailoc-ingress
sudo rm /etc/pf.conf.jailoc-backup

# listener broker: stop it with Ctrl+C first
sudo rm /usr/local/libexec/jailoc-listener-broker     # or /Library/PrivilegedHelperTools/ on macOS
sudo rmdir /var/run/jailoc-proxy/"$(id -u)" /var/run/jailoc-proxy

# names: only the lines that carry the marker (on macOS use: sudo sed -i '' ...)
sudo sed -i '/# jailoc-ingress$/d' /etc/hosts
# macOS wildcard: only if /etc/resolver/jailoc.local holds the two lines written above
sudo rm /etc/resolver/jailoc.local

# trust (macOS)
security remove-trusted-cert "$ING/ca.crt"
security delete-certificate -t -c "jailoc ingress CA" ~/Library/Keychains/login.keychain-db
# trust (Debian, Ubuntu)
sudo rm /usr/local/share/ca-certificates/jailoc-ingress-ca.crt && sudo update-ca-certificates --fresh
# trust (Fedora, RHEL)
sudo rm /etc/pki/ca-trust/source/anchors/jailoc-ingress-ca.crt && sudo update-ca-trust
# trust (Chrome, Chromium on Linux)
certutil -d sql:$HOME/.pki/nssdb -D -n "jailoc ingress CA"

# files created in the ingress directory; the directory is removed only when empty
(cd "$ING" && rm -f ca.ext ca.key ca.csr ca.crt ca.srl demo.ext demo.key demo.csr demo.crt)
rmdir "$ING" 2>/dev/null
```

Also stop `dnsmasq` and remove the CA from Firefox's certificate manager.

## Troubleshoot

| Symptom | Check |
|---------|-------|
| Certificate warning | The CA isn't trusted by this browser; see [Trust the CA](#trust-the-ca). The leaf must list the route's host in `subjectAltName`. |
| Name doesn't resolve | Run the resolver check above; `.local` and secure DNS are the usual causes. Use `/etc/hosts`. |
| Connection refused on 443 | The redirect isn't loaded, or `jailoc proxy` isn't running. |
| `:8443` URL rejected | Open the route without a port. |
| Login code rejected | A code works once and expires 10 minutes after the proxy starts. Restart `jailoc proxy` for a new one. |
| Proxy refuses the workspace | The running VM doesn't match the configured proxy ports. Run `jailoc restart <workspace>`. |
| Route fails after the VM stopped or was replaced | Restart `jailoc proxy`. |
| `the listener broker is not running for this user` | Start `jailoc-listener-broker --uid` with your UID as root; see [Use the listener broker](#use-the-listener-broker-experimental). |
| `another proxy is attached to the listener broker` | Stop the other `jailoc proxy --listener-broker` first. |
| Broker refuses its executable | Install it in a root-owned directory that group and others can't write. |
| Login post or pages behave oddly on a reused route name | An old service worker may control the origin. Unregister it and clear the site data; see [Service workers and origin reuse](#service-workers-and-origin-reuse). |
| Validation error on `ports` or `host_ports` | Guest port 6081 is reserved for the proxy relay. |

## Related

- [`proxy`](../reference/configuration.md#proxy)
- [Security Boundaries](../explanation/security-boundaries.md#https-proxy)
