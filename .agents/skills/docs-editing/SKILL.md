---
name: docs-editing
description: >
  Shorten or restructure jailoc user docs (landing page, tutorials, how-to
  guides, reference, explanation) without losing material facts. Use when a
  docs page is too long, hard to scan, or unclear for first-time readers.
---

# Docs editing

[docs/AGENTS.md](../../../docs/AGENTS.md) is the authority for conventions, quadrants,
voice, heading style and the page-adding checklist. This skill adds the editing workflow.

User docs are the Home, Tutorials, How-to Guides, Reference and Explanation sections of
the `mkdocs.yml` nav. Blog posts are dated records and the Development pages serve
contributors; they stay as they are.

## Steps

1. **Gate the scope.** List the exact user-doc pages to edit. When the user named a task
   rather than files, or moved content needs a destination page, propose the list and wait
   for approval; until a destination is approved, the fact stays on its current page.
   A proposal, plan or skill-only request ends here, before any docs edit.
   Done when the user has approved the page list, or the request ended at this step.

2. **Define the reader.** For each page, write one line in your notes: the reader's task,
   lookup or question, and the action or understanding they need.
   Done when every page has its reader line.

3. **Set the target shape.** Classify each page by its docs/AGENTS.md quadrant, or as the
   landing page (`docs/index.md`), which routes readers rather than teaching:
   - landing page: route to the reader's first action
   - tutorial: shortest successful first run, with enough explanation for first success
   - how-to: the common task first, edge cases after
   - reference: every field, default and constraint, exact and complete
   - explanation: centered on reasons and mechanisms
   Done when each page has its classification and target shape.

4. **Inventory material facts before any cut.** List prerequisite commands, config,
   defaults, errors, security boundaries, data-loss warnings, legal content, and every
   code block or config example. Give each item a verdict:
   - keep: stays on this page
   - move: goes to an approved destination page; a descriptive link stays here
   - link: replaced by a descriptive link, after reading the target section and confirming
     it states every fact in the item
   - delete: obsolete (verified in step 7) or empty text, or text whose every fact the
     surviving page already states; a page's only working example is kept
   Done when every item has a verdict and every move or link has a checked target.

5. **Restructure.** Apply the verdicts:
   - prerequisites come before the command or config that needs them
   - tutorials and how-tos: one action per step, its observable result, then the next
     action; reference and explanation follow their step 3 shape
   - each warning stays next to the action it guards; advanced mechanics move behind links
   - active, concrete wording and descriptive link text; cut repetition
   Done when every inventory item sits at its stated destination.

6. **Preserve identifiers.** Keep exact config keys, flags, commands and page URLs.
   Headings generate anchors. Before renaming, merging or moving a heading, search the
   whole repository for its anchor slug, including absolute `seznam.github.io/jailoc/...#`
   links in `README.md`, `CONTRIBUTING.md` and blog posts. When anything references it,
   keep the heading or put `<a id="old-anchor"></a>` and a blank line above the section
   that now covers the topic. `mkdocs-macros-plugin` renders pages through Jinja, so
   `{#`, `{{` or `{%` anywhere in a page, code blocks included, can replace the page with a
   macro error; `{#id}` heading attributes are therefore unusable.
   Done when every referenced anchor still resolves in the built site.

7. **Verify facts at the source.** Check reworded claims and obsolete deletions against
   code, `--help` output or tests, and record the source in your notes. When docs and code
   disagree, leave the existing claim in place and flag the contradiction for the user.
   Done when every reworded or deleted claim has a recorded source, or is unchanged and flagged.

8. **Validate.** Run the checks that apply to the edit:
   - compare the final text against the inventory
   - run examples through a safe surface; for anything that boots a workspace VM, use the
     [host-validation](../host-validation/SKILL.md) skill and ask before running it
   - build twice in scratch directories outside the repo (a build inside it leaves untracked
     `.cache/`, `.venv/` or `uv.lock`): once from `git archive HEAD docs mkdocs.yml .release-please-manifest.json`
     (baseline), once from a copy of the edited `docs/`, `mkdocs.yml` and
     `.release-please-manifest.json` (the `macros` plugin loads it); in each run
     `uvx --with mkdocs-macros-plugin zensical build --clean`
   - the build exits 0 on warnings; any `Warning:` line absent from the baseline fails
   - check nav and `docs/llms.txt` against the docs/AGENTS.md checklist
   A failed check, or an unresolved fact that affects safe use, makes the work blocked.
   Done when every applicable check passed, or the work is reported as blocked.

9. **Report.** Keep the full inventory in your notes; give the user a concise report:
   material moves and deletions, verification results, unresolved contradictions and
   blockers. Word count is a diagnostic, not a pass gate.
   Done when each move, deletion, check result and open issue is stated once.

## Sources

- [Diataxis: how-to guides](https://diataxis.fr/how-to-guides/)
- [Google developer documentation: procedures](https://developers.google.com/style/procedures)
  and [notices](https://developers.google.com/style/notices)
- [GOV.UK: clear language](https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/clear-language/)
