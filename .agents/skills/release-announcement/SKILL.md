---
name: release-announcement
description: >
  Draft short chat announcement posts for one or more jailoc releases in the
  requested language (for example Czech or English), linking release tags and
  docs. Use when asked to write a release announcement, changelog post,
  Slack/Mattermost release note, or "oznámení releasu".
---

# Release announcement

Short chat post (Slack/Mattermost Markdown) summarising one or more `jailoc` releases for users: what changed for them, why a breaking change happened and what to put in `config.toml`, with deep links into the docs.

## Steps

1. **Resolve inputs**:
   - **Versions**: explicit tag(s) (`2.7.1`, `2.5.0 + 2.6.0`), or the latest tag(s) since the previous announcement (`git tag --sort=-v:refname`).
   - **Language**: the language passed by the user (e.g. `cs` / Czech, `en` / English). If the user did not specify one, ask before drafting.
   Done when target version(s) and language are known.

2. **Gather changes** across the version range:
   - Read `CHANGELOG.md` entries and `git log <prev>..<tag> --oneline` (or `gh release view v<ver>` when a tag is not in the local checkout).
   - Check `docs/llms.txt` and changed files under `docs/` to find the exact documentation URL and `#anchor` under `https://seznam.github.io/jailoc/` for every user-facing feature or config option, plus any matching blog post under `docs/blog/`.
   - Collapse internal refactors, test-only commits, CI/Nix plumbing and follow-up fix commits for a feature introduced in the same range into the parent item.
   Done when every user-visible change maps to one bullet and its doc/blog URL (if documented).

3. **Draft the post** in the requested language following [Format](#format) and [Voice](#voice):
   - Lead with a blog post link if the release ships with one, then **breaking** changes, then features, then notable fixes (or a single grouped fix bullet at the end).
   - Keep the whole post to 4–8 top-level bullets.
   Done when the post is self-contained and every link resolves to a real tag or doc heading.

## Format

```markdown
# jailoc [X.Y.Z](https://github.com/seznam/jailoc/releases/tag/vX.Y.Z) :rocket:

- <item 1> <emoji>
- **breaking** ⚠️ <what changed and link to docs> <emoji>
  - 👆 _<short human note when the change needs attention>_
  - <1-line concrete attack/reason in `code` or prose>
  - <copy-pasteable `config.toml` snippet (including the TOML table) and any activation step such as `jailoc restart <workspace>`>
- <item 3> :custom-emoji:

:package: `brew install --cask seznam/tap/jailoc`
```

- **Header**:
  - Single version: `# jailoc [2.7.0](https://github.com/seznam/jailoc/releases/tag/v2.7.0) :rocket:`
  - Combined releases: `# jailoc [2.7.1](https://github.com/seznam/jailoc/releases/tag/v2.7.1) + [2.7.2](https://github.com/seznam/jailoc/releases/tag/v2.7.2) :rocket:`
  - Major / RC milestone: swap `:rocket:` for `:fire_animated:` and link the blog post as the first bullet (`[`<blog title>`](https://seznam.github.io/jailoc/blog/<slug>/)`).
- **Bullets**: one line per change, starting lowercase (unless starting with a proper noun or inline code), no trailing period, ending with a fitting Unicode or Slack shortcode emoji (`🌐`, `📦`, `🎰`, `⏱️`, `📔`, `🐚`, `㊙️`, `:claude-robot:`, `:docker:`, `:git:`, `:gpg:`, `:settings:`, `:hacker-parrot:`, `:shrek:`).
- **Links**: weave `[anchor text](https://seznam.github.io/jailoc/<path>/#<anchor>)` directly into the sentence rather than pasting bare URLs.
- **Breaking changes**: mark with `**breaking** ⚠️` (or `⚠️ **breaking**`). When users must act, add 1–3 indented sub-bullets: why it was unavoidable, the concrete scenario it defends against, the exact config snippet (with its containing TOML table), and any required activation command (e.g. `jailoc restart <workspace>` when `up`/`reload` cannot apply the change, noting that restarting ends running sessions). Never include real internal company zone or service names in examples; use RFC 2606 placeholders (`corp.example`, `internal.example`).
- **Footer**: always close with `:package: `brew install --cask seznam/tap/jailoc`` (for pre-releases not yet in Homebrew, replace with an italic note linking the release binaries).

## Voice

- Write from the **user's seat**: state what they can now do, what stopped crashing, or what they must add to `config.toml` — not which internal package changed.
- Keep it **punchy and informal**: short clauses, parenthetical asides in `_italics_` or `(:stonks: 64)` when a number or joke lands naturally.
- **Czech (`cs`)**: natural developer Czech as spoken in chat (`fixnutý`, `nově`, `přežije restart`, `zásek`, `udělat X pápá`); keep CLI commands, config keys (`internal_zones`, `paths`), flags and doc headings in English inside backticks.
- **English (`en`)** (or another requested language): same density, lowercase bullet starts, inline doc links, and casual developer tone in the target language.

## Examples

### Patch bundle (cs)

```markdown
# jailoc [2.7.1](https://github.com/seznam/jailoc/releases/tag/v2.7.1) + [2.7.2](https://github.com/seznam/jailoc/releases/tag/v2.7.2) :rocket:

- neexistující cesta v `paths` nezpůsobí skrytý crash VM 🎰
- `migrate` migruje i internal_zones + doporučí jej při deprecated conf :settings:
- dají se přepsat výchozí argumenty harnessu :claude-robot:
- fixnutý limit na 4 klienty paralelně pro jeden ws (:stonks: 64) :hacker-parrot:

:package: `brew install --cask seznam/tap/jailoc`
```

### Minor release with a breaking change (cs)

```markdown
# jailoc [2.7.0](https://github.com/seznam/jailoc/releases/tag/v2.7.0) :rocket:

- síťová pravidla nově i pro [**HTTP metody a cesty**](https://seznam.github.io/jailoc/how-to/network-access/#restrict-http-methods-and-paths), včetně TLS, třeba čtení GL API bez zápisů 🌐
- **breaking** ⚠️ privátní DNS odpovědi potřebují [explicitní `internal_zones`](https://seznam.github.io/jailoc/reference/configuration/#internal-zones) vedle samotných `allow` pravidel
  - 👆 _na tohle pozor, nešlo to bohužel jinak_
  - obrana proti dns rebinding: `povolíte *.example.com a útočník nasměruje foo.example.com na 10.32.200.55 (interní služba) => povolíte tím skrytě i přístup na ni`
  - pod `[defaults.network]` (nebo `[workspaces.<name>.network]`) budete chtít mít něco jako `internal_zones = ["corp.example", "internal.example"]`; běžící VM pak přetočte přes `jailoc restart <workspace>` (ukončí běžící sessions)
  - naopak vnější zóny tam typicky nepatří
- Go, npm, Bun a NuGet mají [persistentní download cache](https://seznam.github.io/jailoc/reference/configuration/#tool-caches), která přežije restart 📦
- opravy background sessions v Claude Code, záseku virtio-fs na macOS :claude-robot:

:package: `brew install --cask seznam/tap/jailoc`
```

### Same minor release in English (en)

```markdown
# jailoc [2.7.0](https://github.com/seznam/jailoc/releases/tag/v2.7.0) :rocket:

- network rules now filter [**HTTP methods and paths**](https://seznam.github.io/jailoc/how-to/network-access/#restrict-http-methods-and-paths), including over TLS — e.g. read-only GitLab API access 🌐
- **breaking** ⚠️ private DNS answers now require [explicit `internal_zones`](https://seznam.github.io/jailoc/reference/configuration/#internal-zones) alongside `allow` rules
  - 👆 _heads up, there was no safe way around this_
  - blocks DNS rebinding: `you allow *.example.com, an attacker points foo.example.com at 10.32.200.55 (an internal service) => you silently allowed that service too`
  - under `[defaults.network]` (or `[workspaces.<name>.network]`), set something like `internal_zones = ["corp.example", "internal.example"]`, then run `jailoc restart <workspace>` for each running workspace (ends running sessions)
  - external zones do not belong there
- Go, npm, Bun and NuGet get a [persistent download cache](https://seznam.github.io/jailoc/reference/configuration/#tool-caches) that survives restarts 📦
- fixes for Claude Code background sessions and a virtio-fs hang on macOS :claude-robot:

:package: `brew install --cask seznam/tap/jailoc`
```
