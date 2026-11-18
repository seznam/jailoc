# How to open a desktop and web services in your browser

A workspace can run a graphical desktop and serve web apps from the VM, and you open both in a browser on the host over HTTPS. `jailoc gateway` is the host-side entry point. jailoc never changes your host's DNS, trust store or firewall; the one-time setup below is manual and uses only commands you run yourself.

The examples use a workspace named `demo` with a web route `foo`. Their addresses are `https://desktop.demo.jailoc.local` and `https://foo.demo.jailoc.local`.

## Limits

- The desktop uses software rendering: no GPU. There is no audio.
- The desktop is a native one in the VM, not a Docker Desktop container, and not your host's Chrome.
- A browser is not preinstalled. The agent installs one.

## Enable the desktop and web routes

```toml
[workspaces.demo]
paths = ["~/projects/demo"]
enable_desktop = true
cpu = 4
memory = "6g"

[workspaces.demo.web]
foo = 3000
```

- `enable_desktop` defaults to `false`.
- Each `web` entry maps a route name to a guest port. The route is served at `https://<name>.<workspace>.jailoc.local`.
- The route `desktop` is reserved. With `enable_desktop = true` it exists on its own and serves the desktop's noVNC client (guest port 6080).
- The workspace name and every route name must be a lowercase DNS label (`a-z`, `0-9`, `-`).
- Software rendering is CPU-bound: start from `cpu = 4` and `memory = "6g"`.

The guest ports in use are fixed: 6080 (noVNC) and 5901 (VNC, loopback only, no X11 over TCP) while the desktop is on, and 6081 (web relay) whenever any route exists. `ports` and `host_ports` can't use them, and a web route can't target a port that `host_ports` also lists. See [`enable_desktop` and `web`](../reference/configuration.md#enable_desktop-and-web).

### Apply changes

| Change | Needed |
|--------|--------|
| `enable_desktop`, or the set of guest ports the `web` routes use | `jailoc restart` for the workspace, then a restart of `jailoc gateway` |
| Adding or renaming a route that uses a guest port already in that set | A restart of `jailoc gateway` only; nothing changes in the guest |

`jailoc gateway` refuses a running VM whose desktop or web ports differ from the configuration, and asks for `jailoc restart` first.

!!! note "Removing routes or the desktop and `jailoc reload`"

    A running VM keeps serving the ports of the desktop (6080, 5901) and of its web routes until it restarts, even after you remove `enable_desktop` or `web` from the configuration. Those ports stay reserved: `jailoc reload` rejects a `host_ports` or `ports` entry that uses one, and asks for `jailoc restart` first.

### Images

- Images jailoc builds (the embedded Dockerfile, a `[base] dockerfile`, or `defaults.image` with a workspace overlay) get a stock `apt` desktop layer when `enable_desktop` is on. The base must be Debian or Ubuntu based.
- An image you set explicitly (`image`, or `defaults.image` without an overlay) gets no layer and must contain the desktop itself: `Xtigervnc`, `websockify`, `openbox`, `dbus-run-session` and noVNC's `vnc.html`. On Debian or Ubuntu these are the packages `tigervnc-standalone-server`, `websockify`, `novnc`, `openbox` and `dbus-x11`. As with `dockerd` for `enable_docker`, `jailoc up` checks the image on the host and fails before the VM boots, naming what is missing. See [Image Resolution](../reference/image-resolution.md#desktop-layer-steps-2-4-and-5).

## Run a browser in the desktop

The desktop is an X11 session on display `:1`. Sessions of the agent get `DISPLAY=:1`, so a GUI program the agent starts appears on the desktop.

No browser is installed. Ask the agent to install one with Playwright, for example `npx playwright install chromium`, and to launch it headful (`headless: false`). Chromium needs system libraries the image may lack; add them in a [custom Dockerfile](custom-images.md) rather than disabling the browser sandbox.

!!! note "Environment of desktop programs"

    The desktop starts at boot with a snapshot of the environment. It holds neither the secrets nor the `env` values delivered after boot, so programs started from the desktop menu don't see them. A program an agent session launches inherits that session's current environment, secrets included, even though its window appears on the desktop.

## Start the gateway

```bash
jailoc gateway demo \
  --tls-cert ~/.config/jailoc/ingress/demo.crt \
  --tls-key ~/.config/jailoc/ingress/demo.key
```

The command runs in the foreground until you stop it. For each route it prints the URL and a login code.

- The listener defaults to `127.0.0.1:8443`. `--listen` takes a loopback IP address and a port; a host name is refused.
- Without workspace arguments the gateway serves every running configured workspace that has routes. A named workspace must be running and have routes.
- Routes close when a workspace VM stops or is replaced. Restart the gateway to serve the new VM.

1. Open `<origin>/.jailoc/login`, for example `https://foo.demo.jailoc.local/.jailoc/login`.
2. Enter the code. The page posts it to the same origin.

The `desktop` route serves the noVNC client, which has no `index.html`: its root and its login redirect to `/vnc.html?autoconnect=1`, so `https://desktop.demo.jailoc.local/` opens the desktop. Other routes, such as `foo`, serve whatever the guest service serves at `/`.

A code works once and expires 10 minutes after the gateway starts. A successful login sets a `__Host-` cookie that lasts 8 hours. Every request must carry the route's `Host` and a matching TLS server name. A `GET` or `HEAD` needs no `Origin` header, but one that is present must be exactly the route's origin. Every other method, and every WebSocket upgrade, requires that exact `Origin`; cross-site requests are refused. Open the route without a port, or with `:443`; `:8443` is rejected.

The gateway reaches the guest through a private socket (`web.sock`) on the host and the guest's web relay; nothing is published on the host except the listener you chose. See [Security Boundaries](../explanation/security-boundaries.md#web-gateway).

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

- `*.demo.jailoc.local` covers `foo.demo.jailoc.local` and `desktop.demo.jailoc.local`. A wildcard covers one label, so `*.jailoc.local` doesn't: issue one leaf per workspace, or list several `DNS:*.<workspace>.jailoc.local` names in one certificate.
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

Point the route names at `127.0.0.1`, where the redirect and the gateway listen.

**Exact names in `/etc/hosts` (macOS and Linux).** List every route, including `desktop`, and add a name after each new route. Check first that no line of yours already carries the marker:

```bash
grep -c '# jailoc-ingress' /etc/hosts    # 0 expected
echo '127.0.0.1 desktop.demo.jailoc.local foo.demo.jailoc.local # jailoc-ingress' | sudo tee -a /etc/hosts
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

### Redirect port 443 to the gateway

An unprivileged process can't listen on 443, so the gateway listens on 8443 and the host redirects loopback port 443 to it. Redirect only `127.0.0.1` port 443; don't redirect other traffic.

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

## Verify the setup

With the gateway running, request the login page with the CA as the only trust anchor and the name pinned to loopback. No `-k`: a certificate failure must fail the command.

```bash
curl --cacert "$ING/ca.crt" --resolve foo.demo.jailoc.local:443:127.0.0.1 \
  https://foo.demo.jailoc.local/.jailoc/login -sS -o /dev/null -w '%{http_code}\n'
```

`200` shows that the certificate chain, the 443 redirect and the gateway work. It doesn't test DNS or the browser's trust store; run the resolver check above, then open the page in the browser.

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
| Connection refused on 443 | The redirect isn't loaded, or `jailoc gateway` isn't running. |
| `:8443` URL rejected | Open the route without a port. |
| Login code rejected | A code works once and expires 10 minutes after the gateway starts. Restart `jailoc gateway` for a new one. |
| Gateway refuses the workspace | The running VM doesn't match the configured desktop or web ports. Run `jailoc restart <workspace>`. |
| Route fails after the VM stopped or was replaced | Restart `jailoc gateway`. |
| `jailoc up` names missing desktop programs | The explicit `image` lacks them; see [Images](#images). |
| Validation error on `ports` or `host_ports` | Guest ports 6081, and 6080 and 5901 with the desktop, are reserved. |

## Related

- [`enable_desktop` and `web`](../reference/configuration.md#enable_desktop-and-web)
- [`jailoc gateway`](../reference/cli.md#jailoc-gateway)
- [Security Boundaries](../explanation/security-boundaries.md#web-gateway)
