---
name: host-validation
description: >
  Validate jailoc features end to end by booting real workspace VMs on a local
  or remote (SSH) macOS or Linux/KVM host, isolated from the user's own
  workspaces. Use when a change needs proof beyond unit tests (VM boot, shares,
  protect_git, network policy, DNS filtering, reload, secrets, migrate,
  Landlock), or when asked to test a branch or PR on a host.
---

# Host validation

Unit tests run anywhere; this skill proves behaviour inside **real VMs**. Every
run is **isolated**: its own `HOME`, config and cache under a scratch directory,
so the user's workspaces, VMs and caches stay untouched. Results go into a local
report, one row per check.

## Steps

1. **Pick the host and commit.** Local, or `ssh -o BatchMode=yes <host>`. Record
   OS, kernel (`uname -r`), arch, Docker version, and on Linux `/dev/kvm` access
   and Landlock (`cat /sys/kernel/security/lsm`). List running VMs of the user
   (`jailoc list`, `pgrep -af jailoc-`) so you can leave them alone.
   Done when the report header names host, OS/kernel, commit SHA and the user's
   running VMs.

2. **Build at the commit** in a clone outside the user's checkout (remote: clone
   or `git fetch origin <sha> && git checkout --detach FETCH_HEAD`):

   ```bash
   go generate ./internal/guestbin   # embeds jailoc-guest; without it the VM cannot boot
   go build -o $S/jailoc ./cmd/jailoc
   ```

   Done when `$S/jailoc --help` runs and `git rev-parse HEAD` in the clone is the target SHA
   (a plain `go build` leaves `--version` without the commit).

3. **Isolate.** Write `$S/env.sh` and source it in every shell/SSH script:

   ```bash
   export S=/path/to/scratch HOME=$S/home XDG_CONFIG_HOME=$S/home/.config XDG_CACHE_HOME=$S/home/.cache
   export PATH=/usr/local/go/bin:$PATH J=$S/jailoc   # call $J by absolute path; ~ now points into $S
   ```

   Done when `$J list` shows no workspaces.

4. **Provide the VM runtime.** Release builds embed it. A dev build needs
   `[vm] runner` and `lib_dir` in `$XDG_CONFIG_HOME/jailoc/config.toml`; reuse a
   runtime a release jailoc already unpacked:

   ```toml
   [vm]
   runner = "<real home>/.cache/jailoc-vm/runtime/<hash>/runtime/jailoc-vm-runner"
   lib_dir = "<real home>/.cache/jailoc-vm/runtime/<hash>/runtime:<real home>/.cache/jailoc-vm/runtime/<hash>/firmware"
   ```

   Otherwise build one (`docs/development.md`, "Building and testing"). A runner
   must match the go-microvm revision in `go.mod` when the change touches
   go-microvm options (`task build-runner` in a go-microvm checkout at that
   revision; keep the unpacked runtime's patched libkrun in `lib_dir`).
   Done when a minimal workspace (`paths = ["$S/proj"]`) reaches `running` in
   `$J up` and `$J exec -w <ws> -- id </dev/null` prints `uid=1000(agent)`.

5. **Run the checks** from [checks.md](checks.md) that the change touches, plus
   the boot smoke test. Each check states its setup, commands and expected
   result. Long jobs (test suites, `vmintegration`) run in the background with
   a log ending in an `EXIT <code>` line; poll the log.
   Done when every selected check has PASS or FAIL with the command and the
   output that decided it.

6. **Diagnose failures before reporting.** Read `$XDG_CACHE_HOME/jailoc-vm/workspaces/<ws>/`
   (`supervisor.log`, `microvm/console.log`, `microvm/vm.log`, `broker.jsonl`)
   and re-run the step on `main` to tell a regression from an existing problem.
   Done when each FAIL names its cause or says "reproduces on main".

7. **Clean up**: `$J down --purge <ws>` for every scratch workspace, remove
   scratch Docker volumes, then `chmod -R u+w $S && rm -r $S` (the Go module
   cache is read-only). Done when `pgrep -af jailoc-` lists only the user's own
   VMs from step 1.

## Gotchas

- `jailoc exec` reads stdin: inside `ssh host 'bash -s' <<'EOF'` scripts give
  every `$J exec` a `</dev/null`, or it swallows the rest of the script.
- `jailoc exec -w <ws> -- cmd`, `jailoc sessions -w <ws>`; `shell`, `traffic`,
  `strace`, `logs`, `up`, `down`, `reload` take the workspace as an argument.
- Guest `sudo` is disabled with `protect_git = true`; use a second workspace
  without it for root-in-guest checks.
- `internal/vmintegration` hard-codes `dns_upstream = "1.1.1.1"`. Where the host
  network blocks it, the DNS-dependent subtests fail; point it at the host's
  resolver in an uncommitted local edit, rerun, then revert. Run it with
  `JAILOC_VM_RUNNER`/`JAILOC_VM_LIBDIR` set to the runtime from step 4 and
  `-timeout 45m`.
- Containers and small hosts have a tiny `/tmp`: set `TMPDIR` and `GOTMPDIR` to
  a roomy directory. On macOS keep `TMPDIR` short (`/tmp`): Unix socket paths
  are limited to 104 bytes.
- The guest's process limit stops bash fork loops early; open many connections
  from one Python process instead.
- `TestConfinedRunnerAccess` skips without Landlock (containers): Landlock
  results only count from a real Linux host.
- The runner appears in `ps` as `jailoc-<workspace>`; its second argument is
  the VM config JSON.
