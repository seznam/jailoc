---
name: terminal-screenshots
description: >
  Regenerate the terminal screenshots of `jailoc traffic`, `jailoc strace`,
  `jailoc sessions` and `jailoc shell` output used in the docs and blog (docs/blog/img/*.png)
  without booting a VM. Use when adding, refreshing or restyling those images,
  or after the traffic/strace/sessions output format changes.
---

# Terminal screenshots

The images are sample data rendered through jailoc's **real formatters**, so the
layout and colours match the shipped CLI exactly; only the events are made up.
A VM is never booted, so this works on hosts without KVM.

Pipeline: [`shots_test.go`](shots_test.go) (Go, package `cmd`) feeds events
through `trafficFormatter` / `execFormatter` from `internal/cmd/inspectfmt.go`
and replicates the `listSessionsVM` table from `internal/cmd/vmsession_unix.go`;
the `jailoc shell` transcript is written by hand.
It writes transcripts `traffic.ansi`, `strace.ansi`, `sessions.ansi` and
`shell.ansi`: lines starting with `$ ` are prompts, the rest is command output. [`render.mjs`](render.mjs) turns each transcript into a 2x
macOS-style dark terminal window PNG with Playwright/Chromium.

## Steps

Run from the repo root. `W` is a scratch dir outside the repo.

1. **Generate ANSI** (the driver is temporary; it lives in `internal/cmd` only while it runs):

   ```bash
   W=/tmp/opencode/shots
   cp .agents/skills/terminal-screenshots/shots_test.go internal/cmd/zz_shots_test.go
   SHOTS_DIR=$W TZ=Europe/Prague go test ./internal/cmd -run TestZZShots -count=1
   rm internal/cmd/zz_shots_test.go
   ```

   `TZ=Europe/Prague` is load-bearing: sample times are UTC and render as 14:31 CET.
   Done when `$W` holds four non-empty `.ansi` files and `git status internal/` is clean.

2. **Prepare the renderer** (once per machine):

   ```bash
   mkdir -p $W/render && cd $W/render
   npm i --no-audit --no-fund playwright@1.48.0
   cp <repo>/.agents/skills/terminal-screenshots/render.mjs .
   PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/pw-shots npx playwright install chromium
   ```

   Keep browsers under `~/.cache`: `/tmp` can be a small tmpfs that Chromium does not fit in.
   If `playwright install` fails while extracting, download the zip URL it prints and
   `unzip` it into `$PLAYWRIGHT_BROWSERS_PATH/chromium-<revision>/` (1.48.0 → `chromium-1140`).

3. **Render** (needs network for the JetBrains Mono web font):

   ```bash
   cd $W/render
   PLAYWRIGHT_BROWSERS_PATH=$HOME/.cache/pw-shots node render.mjs $W <repo>/docs/blog/img
   ```

   Writes `<name>.png` per `<name>.ansi`. The first prompt titles the window; a
   transcript without a trailing newline ends with the cursor on its last line
   (the guest shell prompt); one whose last prompt ends in ` -f` ends on a bare
   cursor (still following); others on a fresh `$` prompt.

4. **Check** every PNG by viewing it: colours present, no wrapped lines, font is
   JetBrains Mono (a fallback font means the font request failed).

5. **Clean up**: `rm -r $W`. Leave `~/.cache/pw-shots` for the next run or delete it.

## Changing the story

The samples tell one consistent story that the blog captions in
`docs/blog/jailoc-2.md` describe. Change events, PIDs or times together across
all outputs and update the captions and alt texts to match.

- `traffic`: OpenCode in workspace `shop` reaches `api.anthropic.com`, npm, GitHub
  over SSH, an internal GitLab allowed by `exception 10.20.4.17/32`, Debian over
  plain HTTP; then `postgres.prod.corp.example` is blocked by `filtered_dns`,
  `10.20.9.3` hits `denied-range`, the metadata IP hits `hard-deny`; finally an
  inbound connection to the published dev server port 5173.
- `strace`: the processes behind that traffic, one tree under `opencode` (PID 812,
  parent 788 is jailoc's own hidden session process). Every recorded process gets
  an exit record; status 0 exits are hidden by the formatter, so only the failed
  `psql` (exit 2) and `curl` (exit 7, the broker resets denied connections) show.
- `sessions`: one `jailoc sessions` listing five sessions with different
  harnesses (OmO, OpenCode, Copilot CLI, Claude Code, OpenCode `--continue`),
  attached and detached.
- `shell`: `jailoc shell` in `shop` with Ubuntu's coloured bash prompt
  (`agent@<workspace>:<host path>$`; the VM hostname is the workspace name):
  `id`, the `/run/secrets` file secret, the npm registry reachable, the
  `filtered_dns` name not resolving (`getent` exit 2, the broker answers
  NXDOMAIN) and the metadata IP refused (`curl: (7)`).

Keep event fields shaped like the real broker emits them: DNS `type` is `TypeA`
(not `A`), names carry a trailing dot, flow `reason` is one of the broker's
reasons (`public`, `exception <CIDR>`, `denied-range`, `hard-deny`, …) and
session IDs are 6 hex characters. Agent command lines are deliberately short
(binary name, plus `--continue`): the real ones also carry unattended-mode and
plugin flags (`--dangerously-skip-permissions`, `--settings …`), which only add noise.
Read `internal/netbroker` and `internal/guest` when adding a new event kind.

If the formatter output changes (a new column, different colours), just rerun the
steps; if the `sessions` table changes, update the hand-made copy in
`shots_test.go`.
