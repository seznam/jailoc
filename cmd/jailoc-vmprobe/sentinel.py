#!/usr/bin/python3
"""vmprobe-compatible sentinel for macOS hosts with the application firewall on.

The macOS application firewall silently drops inbound connections to unsigned
binaries (such as a locally built jailoc-vmprobe) on non-loopback addresses,
which makes every host-address deny check vacuous. Apple-signed /usr/bin/python3
is allowed by default, so this script speaks the same wire protocol as
`jailoc-vmprobe sentinel` (reply "JLS1 <name>\\n", JSONL hit log) and can replace
it on macOS.

Usage: /usr/bin/python3 sentinel.py NAME LOG PORT [PORT...]
"""

import datetime
import json
import socket
import sys
import threading

NAME, LOG, PORTS = sys.argv[1], sys.argv[2], [int(p) for p in sys.argv[3:]]
LOCK = threading.Lock()


def fmt(addr):
    host, port = addr[0], addr[1]
    if host.startswith("::ffff:") and "." in host:
        host = host[7:]
    return f"[{host}]:{port}" if ":" in host else f"{host}:{port}"


def record(proto, local, remote, data):
    line = data.split(b"\n", 1)[0][:120].decode("utf-8", "replace")
    hit = {
        "time": datetime.datetime.now(datetime.timezone.utc)
        .isoformat()
        .replace("+00:00", "Z"),
        "sentinel": NAME,
        "proto": proto,
        "local": fmt(local),
        "remote": fmt(remote),
        "payload": line,
    }
    with LOCK, open(LOG, "a") as f:
        f.write(json.dumps(hit) + "\n")


def dual(kind):
    s = socket.socket(socket.AF_INET6, kind)
    s.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
    s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    return s


def handle(conn, remote):
    with conn:
        conn.settimeout(3)
        data = b""
        try:
            while b"\n" not in data and len(data) < 512:
                chunk = conn.recv(512)
                if not chunk:
                    break
                data += chunk
        except OSError:
            pass
        record("tcp", conn.getsockname(), remote, data)
        try:
            conn.sendall(f"JLS1 {NAME}\n".encode())
        except OSError:
            pass


def serve_tcp(port):
    s = dual(socket.SOCK_STREAM)
    s.bind(("::", port))
    s.listen(128)
    while True:
        conn, remote = s.accept()
        threading.Thread(target=handle, args=(conn, remote), daemon=True).start()


def serve_udp(port):
    s = dual(socket.SOCK_DGRAM)
    s.bind(("::", port))
    while True:
        data, remote = s.recvfrom(2048)
        record("udp", s.getsockname(), remote, data)
        s.sendto(f"JLS1 {NAME}\n".encode(), remote)


for p in PORTS:
    threading.Thread(target=serve_tcp, args=(p,), daemon=True).start()
    threading.Thread(target=serve_udp, args=(p,), daemon=True).start()
print(f"sentinel {NAME} listening on {PORTS}", flush=True)
threading.Event().wait()
