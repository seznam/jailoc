# Checks

Each check: **Setup** (config under `[workspaces.<ws>]` and host files in `$S`),
**Run**, **Expect**. Guest commands run through `$J exec -w <ws> -- sh -c '…' </dev/null`
unless marked host. Copy a check's wording into the report together with the
actual output.

## Build and tests

- **Run (host):** `go build ./...`, `go test ./...`; on Linux also
  `go test ./internal/vm -run TestConfinedRunnerAccess -v` and
  `go test -tags vmintegration -count=1 -timeout 45m ./internal/vmintegration/`.
- **Expect:** all pass; `TestConfinedRunnerAccess` runs its subtests instead of
  skipping (only the Unix-socket subtest skips below Landlock ABI 6).

## Boot and status

- **Setup:** `paths = ["$S/proj"]`.
- **Run (host):** `$J up <ws>`, `$J status <ws>`; guest `id; cat /proc/mounts`.
- **Expect:** `running` with the configured CPUs and memory; `uid=1000(agent)`;
  the workspace path mounted at the same path as on the host.

## Read-only internal share

- **Setup:** `snapshots = ["$S/snap.txt:/home/agent/snap.txt"]`, `protect_git = false`.
- **Run:** guest `grep jailocro /proc/mounts`; as root write into
  `/.jailoc/private/host-ro/snapshots/*/snap.txt`; `mount -o remount,rw` it;
  mount `jailocro` fresh at `/mnt/ro` and write there. Host: checksum
  `$XDG_CACHE_HOME/jailoc-vm/workspaces/<ws>/guest-ro/` before and after.
  Edit `$S/snap.txt`, `$J reload <ws>`, read `~/snap.txt`.
- **Expect:** every write fails with `Read-only file system` even as root and
  after remount (the host side enforces it); host checksums unchanged; after
  reload the guest sees the new content.

## Snapshot reload

- **Setup:** add a snapshot with target `/home/agent/sub/s.txt`.
- **Run:** guest as root: `mkdir /home/agent/x && mount -t tmpfs none /home/agent/x`,
  then `mv /home/agent/sub /home/agent/sub.old && ln -s /home/agent/x /home/agent/sub`;
  host `$J reload <ws>` twice. Then make `/home/agent/snap.txt` a symlink (to
  `/etc/hostname`) and, separately, a FIFO; reload each time.
- **Expect:** the tmpfs at `/home/agent/x` stays mounted (`findmnt`); a
  symlink or FIFO target fails reload with "destination exists as a different
  file type" and nothing is mounted over the link target.

## protect_git

- **Setup:** `protect_git = true`, `paths = ["$S/proj"]`, `mounts = ["$S/data:/data"]`,
  `git init` in `$S/proj` and `$S/data/repo`.
- **Run:** guest as agent: write `.git/hooks/x` and append to `.git/config` in
  both repos, and write a worktree file. Host: set `core.hooksPath = tools/hooks`
  and `include.path = ../extra.gitconfig` (create both), `$J restart <ws>`,
  write to them from the guest. Replace `.git/config` with a symlink, restart.
- **Expect:** Git writes fail with `Read-only file system`, worktree writes
  succeed; the `up` message counts the protected repositories; `sudo` is
  refused; the symlinked config makes `up` fail with "symbolic link".

## Filtered DNS

- **Setup:** `filtered_dns = true`, `dns_upstream = "<resolver reachable from the host>"`,
  `dns_blocked_zones = ["example.org", "akamaiedge.net"]`.
- **Run:** guest `getent ahostsv4 www.example.org`, a public name whose CNAME
  chain ends in a blocked zone (`www.microsoft.com`, `www.apple.com` end in
  `akamaiedge.net`), and `example.com`. Host `$J traffic <ws>`.
- **Expect:** the blocked names and the CNAME-chained names return nothing;
  `example.com` resolves; traffic shows `BLOCK dns … NXDOMAIN (filtered_dns)`.

## Broker limits and log rotation

- **Run:** guest Python opening and holding ~5000 TCP connections to a public
  `host:443` (two processes of 2600 each, to stay under the guest process
  limit); while held, `curl` something; afterwards `curl` again. Host:
  `$J traffic <ws>`, broker FD count (`ls /proc/<broker pid>/fd | wc -l`).
  Separately send UDP to private addresses (`10.0.0.0/8`, all ports) until
  `broker.jsonl.1` appears in the state directory.
- **Expect:** exactly 4096 connections succeed, the rest fail; traffic shows
  `LIMIT  tcp … too many open connections`; after release `curl` works and FDs
  drop back. `broker.jsonl.1` stays under 16 MiB and `$J traffic` keeps printing.

## Exec log redaction

- **Setup:** `[workspaces.<ws>.secrets.env.MYTOKEN] from_file = "$S/secret.txt"`
  (value of 8+ characters).
- **Run:** guest `/bin/echo $MYTOKEN; /bin/true $MYTOKEN; /bin/sleep 1 x$MYTOKEN`.
  Host: `grep -c <value> …/guest/control/exec.jsonl`, `$J strace <ws> | tail`.
- **Expect:** zero occurrences; argv shows `[redacted]`, also inside a
  longer argument (`x[redacted]`).

## Host reads of guest-written files

- **Run:** guest `mkfifo ~/.config/opencode/tui.json` (agent config mount), host
  `$J restart <ws>`. Guest `mkdir -p ~/.cache/deny/sub && chmod 000 ~/.cache/deny/sub ~/.cache/deny`,
  host `$J down --purge <ws>`. Guest: write `/run/jailoc/sessions/abcd.json` with
  `{"id":"abcd","name":"a\u001b]0;PWNED\u0007b","dir":"/x\u001b[31mred","pid":1}`,
  host `$J sessions -w <ws> | cat -v`.
- **Expect:** restart warns "not a regular file" and boots without hanging; the
  state directory is gone after purge; the session row shows `\x1b` escapes.

## Nested share symlinks

- **Setup:** `paths = ["$S/vproj"]`, `mounts = ["$S/vproj/link:/x"]` with
  `link -> $S/vother`; also a variant listing the path through a symlinked alias
  of `$S/vproj`.
- **Expect:** `up` refuses with "is a symbolic link inside the read-write share"
  and the VM never starts.

## Landlock (Linux)

- **Run:** with a VM up, find the runner (`pgrep -af jailoc-<ws>`), check
  `NoNewPrivs: 1` in `/proc/<pid>/status`; exercise the VM (write files, list
  mounts, `curl` a public URL, reload).
- **Expect:** everything works and `supervisor.log`, `microvm/vm.log`,
  `microvm/console.log` contain no `EACCES` or `Permission denied`. Report any
  denied path in full: the rule set misses something the runtime needs.

## Migration from v1

- **Setup (host, Docker):** a separate isolated env with a v1-style workspace
  `m`; Docker volume `jailoc-m_opencode-data-m` holding `storage/session.json`,
  an in-tree symlink, `linkout -> /etc/hosts`, and `sub/esc -> $S/escape`. On a
  case-insensitive macOS file system add `Out -> /tmp` and `out/child`.
- **Run:** `$J migrate -y`.
- **Expect:** the volume lands in `…/workspaces/m/guest/home/opencode-data`
  with symlinks kept as symlinks; nothing appears outside it (`$S/escape`
  absent, `/etc/hosts` checksum unchanged); the case collision aborts with
  "path escapes from parent".
