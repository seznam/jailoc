# docs/AGENTS.md

## Documentation Stack

- **Framework**: [Diátaxis](https://diataxis.fr/) — four documentation modes, each with distinct purpose and voice
- **Generator**: MkDocs with Material theme (`mkdocs.yml`)
- **Plugins**: `macros` (template variables like `{{ version }}`)
- **Extensions**: `admonition`, `pymdownx.details`, `pymdownx.superfences` (with Mermaid custom fence)
- **Custom CSS**: `stylesheets/extra.css` — hero image, code block, table, and typography overrides
- **Build**: `zensical` + `mkdocs-macros-plugin` → GitHub Pages on `v*` tags

## Diátaxis Structure

Each page belongs to exactly one quadrant. Do not mix purposes within a page.

| Directory | Quadrant | Purpose | Voice |
|-----------|----------|---------|-------|
| `tutorials/` | Learning-oriented | Walk the reader through doing something for the first time | "Follow along with me…" — second person, step-by-step, no unexplained jumps |
| `how-to/` | Goal-oriented | Solve a specific, already-understood problem | "Do X, then Y" — imperative, assumes context, minimal explanation |
| `reference/` | Information-oriented | Exact technical descriptions of every field, flag, and behaviour | Dry, complete, consistent structure — tables, no narrative |
| `explanation/` | Understanding-oriented | Explain why things work the way they do | Discursive prose, assumes the reader wants to understand, not act |

### Current pages

```
docs/
  index.md                         Landing page — hero image, Mermaid overview diagram, link hub
  development.md                   Contributor guide — build, test, CI, conventions
  tutorials/
    getting-started.md             Install + first workspace walkthrough
  how-to/
    installation.md                go install + pre-built binaries
    workspace-configuration.md     Add workspaces, set paths, per-workspace options
    custom-images.md               URL Dockerfile presets, local Dockerfiles, build context
    network-access.md              allowed_hosts, allowed_networks, verifying rules
    ssh-git-passthrough.md         SSH agent forwarding, Git config and known_hosts mounting
    access-modes.md                remote vs exec mode configuration
  reference/
    cli.md                         All subcommands with flags and examples
    configuration.md               Every config.toml field, types, defaults, validation rules
    image-resolution.md            5-step resolution cascade, step-by-step
  explanation/
    overview.md                    Startup sequence, package layout, two-container rationale
    container-architecture.md      Mermaid diagram, volumes, entrypoint phases, dind rationale
    network-isolation.md           iptables rules, security model
    access-modes.md                Remote vs exec — why two modes exist
```

## Writing Conventions

### Language and tone
- English only (project was migrated from Czech)
- Second person for tutorials and how-to ("you"), third person for reference and explanation
- Imperative mood for how-to headings ("Configure network access", not "Configuring network access")
- No filler sentences ("In this section we will…") — get to the point
- No temporal language — describe behaviour as timeless fact, not as a change from a previous state. Avoid "now", "used to", "no longer", "previously", "newly", "has been added", "was replaced". Write as if the reader has no knowledge of prior versions.
- Do not document what is safe or harmless — only document restrictions, warnings, and breaking changes. Assume anything not mentioned is acceptable.

### Formatting
- `#` for page title, `##` for main sections, `###` for subsections — no deeper than `####`
- Code blocks with language hint: ` ```bash `, ` ```toml `, ` ```go `
- Tables for structured reference data (config fields, CLI flags, port assignments)
- Admonitions for warnings and notes: `!!! warning`, `!!! note`, `!!! danger` — not blockquotes
- Mermaid diagrams for architecture visuals: ` ```mermaid ` (superfences renders them)
- Internal cross-references use relative paths: `[text](../how-to/network-access.md)`

### Template variables
- `{{ version }}` expands to `$GITHUB_REF_NAME` or `"latest"` — use it in install commands
- Provided by the `macros` plugin via `extra.version` in `mkdocs.yml`

### Adding a new page
1. Create the `.md` file in the correct Diátaxis directory
2. Add it to `nav:` in `mkdocs.yml` under the matching section
3. Add a link from `index.md` in the appropriate section
4. Cross-link from related pages where it aids navigation

### Updating existing pages
- When a code change affects user-facing behaviour, update **all** docs that describe it
- Reference pages must stay in sync with code — field names, defaults, validation rules
- If a how-to references a feature that changed, verify the steps still work
- When adding, removing, or renaming a doc page, update `docs/llms.txt` — it lists all pages with descriptions following the [llmstxt.org](https://llmstxt.org/) spec
