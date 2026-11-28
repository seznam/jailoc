# jailoc 2.11: open workspace web apps over HTTPS

*Release 2.11*

When your agent starts a web app inside a jailoc workspace, you can open it in your usual browser without moving the app out of the VM. The jailoc 2.11 workspace proxy gives each configured app its own local HTTPS hostname.

Set up the host with [`jailoc proxy install`](https://github.com/seznam/jailoc/pull/498). It shows what it will change before asking for confirmation and uses `sudo` for the privileged steps. Starting a workspace alone doesn't change your host's name resolution, trust stores or firewall.

## From a guest port to a browser tab

Say the app runs on port 3000 inside the `demo` workspace. Give it a route name in your jailoc configuration:

```toml
[workspaces.demo]
paths = ["~/projects/demo"]

[workspaces.demo.proxy]
web = 3000
```

```bash
jailoc proxy install
jailoc up demo
```

Once the app is running in the VM, open `https://web.demo.jailoc.local` in your host browser. There's no port to type. The name `web` picks a guest port; `demo` picks the workspace. The host listener is loopback-only, so this doesn't publish the app on your LAN. Routes don't require a login code by default; use `web = { port = 3000, auth = true }` if you want one.

## What the installer changes

Three things have to line up for that URL to work:

- **The name resolves locally.** jailoc writes the configured workspace and route names into a managed block in `/etc/hosts`, leaving unrelated entries alone. Run `jailoc proxy install` again after adding or renaming routes to sync the names. `jailoc reload` applies route changes to a running workspace; it doesn't edit `/etc/hosts`.
- **Something listens on port 443.** A root-owned listener broker runs as a LaunchDaemon on macOS or a systemd service on Linux. It listens only on IPv4 and IPv6 loopback and hands connections to your unprivileged jailoc proxy over a private socket. The broker doesn't handle TLS or parse HTTP. That separation is why you don't need a firewall redirect or to run the proxy itself as root.
- **The client trusts the certificate.** jailoc installs its local CA in your macOS login keychain or a supported Linux system trust store, and in supported browser NSS databases when `certutil` is available. The CA and its intermediate are name-constrained to `jailoc.local`; this is still a change to your host's trust settings. On Linux, a successful `curl` doesn't mean Firefox or Chromium trusts the CA. If the browser still warns, check the [browser trust setup](../how-to/proxy-routes.md#install-the-host-setup).

This is traffic **from the host browser into a configured guest service**, not a change to the network rules that govern where your agent can connect from the VM.

## Check the proxy without checking the app

The bare workspace URL gives you a way to tell whether you're debugging the proxy or your app:

```bash
jailoc proxy status
curl https://demo.jailoc.local/
```

`jailoc proxy status` checks the background proxy, broker reachability, CA trust and missing host entries. The bare URL returns `200 OK` with the body `OK` without contacting a guest service. If that works but `https://web.demo.jailoc.local` doesn't, check the app and its configured guest port next.

## Undo the host setup

If you stop using the automated setup, `jailoc proxy uninstall` asks for confirmation, then removes the listener service, the managed hosts entries and the trust changes it made. The [proxy routes guide](../how-to/proxy-routes.md) covers browser-specific setup, troubleshooting and the manual alternative.
