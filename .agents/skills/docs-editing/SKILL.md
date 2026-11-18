---
name: docs-editing
description: >
  Edit, shorten or restructure jailoc user docs (landing page, tutorials,
  how-to guides, reference, explanation) without losing material facts.
  Use when asked to rewrite, trim, simplify or reorganize pages under docs/,
  or to make a docs page clearer for first-time readers.
---

# Docs editing

Conventions, quadrants, page list, formatting and the page-adding checklist live in
[docs/AGENTS.md](../../../docs/AGENTS.md). It is the authority; this skill adds only the
editing workflow and never restates those rules.

## Steps

1. **Gate the scope.** Name the pages or task the user explicitly requested.
   Done when every page you may edit is listed; anything unlisted stays untouched.
   A proposal, plan or skill-only request stops here, before any docs edit.

2. **Define the reader.** Write one line: the reader's task, lookup or question, and the
   successful action or understanding they need.
   Done when you can say what the reader can do or understand after the page that they could not before.

3. **Classify the page.** Pick the quadrant using docs/AGENTS.md and shape it. Content pages
   take exactly one quadrant; a landing page routes readers and is never forced into one:
   - landing page: route to the reader's first action
   - tutorial: shortest successful first run, with enough explanation for first success
   - how-to: the common task first, edge cases after
   - reference: every field, default and constraint, exact and complete
   - explanation: centered on reasons and mechanisms
   Done when the page has its quadrant (or is marked landing) and its shape is stated.

4. **Inventory material facts before any cut.** List prerequisite commands, config,
   defaults, errors, security boundaries, data-loss warnings and legal content.
   Done when each fact has a verdict: keep, move (with destination), link, or delete.
   Delete only text that is obsolete, empty, or duplicated by surviving canonical coverage.

5. **Restructure.** Apply the verdicts:
   - prerequisites come before the command or config that needs them
   - in tutorials and how-tos: one action per step, expected result, then the next action;
     reference and explanation keep their own shape (no uniform procedural layout), and
     their expected result is that the reader finds or understands
   - keep a warning next to the action it guards; link out for advanced mechanics, never for an essential warning
   - use active, concrete wording; put the descriptive word first in headings and link text; delete filler and repetition
   Done when every inventory item sits at its stated destination.

6. **Preserve identifiers.** Keep exact config keys, flags, commands, existing URLs and
   anchors. When a heading changes, add an explicit anchor that keeps the old one.
   Done when a search for each old anchor and identifier finds it still resolvable.

7. **Verify facts at the source.** Check claims against code, `--help` output or tests;
   never guess. Report contradictions between docs and source instead of choosing a side.
   Done when each changed claim cites its source or is flagged as unresolved.

8. **Validate.** Run the checks that apply to the edit:
   - compare the final text against the fact inventory
   - run examples through a safe surface; ask before heavy host or VM actions
   - build with the project's existing docs config and command (see docs/AGENTS.md and the Docs workflow); add no lint, dependency or CI
   - resolve links, anchors and `mkdocs.yml` navigation; update `docs/llms.txt` if pages were added, removed or renamed
   Done when each applicable check passed. A failed check, or an unresolved fact that affects
   safe use, blocks completion: report the work as blocked, not done.

9. **Report.** Keep the full coverage inventory internal and exhaustive; give the user a
   concise report: material moves and deletions, verification results, unresolved
   contradictions and blockers. Do not list every fact. Word count is a diagnostic, not a pass gate.
   Done when each move, deletion, check result and open issue is stated once.

## Sources

- [Diataxis: how-to guides](https://diataxis.fr/how-to-guides/)
- [Google developer documentation: procedures](https://developers.google.com/style/procedures)
  and [notices](https://developers.google.com/style/notices)
- [GOV.UK: clear language](https://guidance.publishing.service.gov.uk/writing-to-gov-uk-standards/writing-guidelines/clear-language/)
