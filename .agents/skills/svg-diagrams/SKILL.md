---
name: svg-diagrams
description: >
  Draw or update hand-written SVG diagrams for the docs and blog in the style of
  docs/explanation/img/architecture.svg (white card, tinted lanes, accent-barred
  boxes, mono component names, legend, dashed boundary with a pill). Use when a
  page needs a new architecture, data-flow or sequence picture, or when an
  existing SVG diagram must change because the system it shows changed.
---

# SVG diagrams

Diagrams are plain SVG files written from a throwaway generator script, so
positions come from arithmetic instead of hand-placed numbers, and every
diagram shares one visual language: [style.md](style.md) holds the tokens and
element recipes, [architecture.svg](../../../docs/explanation/img/architecture.svg)
is the reference rendering.

## Steps

1. **Decide the story** in one sentence (the SVG `<desc>`), the lanes (where
   things run: host, VM, network, …) and the 4–10 components with a name, a
   one-line role and at most two body lines each. Every component and arrow
   must match the code: read the packages behind them.
   Done when each component and arrow can be traced to a file or function.

2. **Generate** with a scratch Python script outside the repo that defines
   helpers for card, legend, lane, box, chip, arrow, pill and boundary from
   [style.md](style.md), lays out a grid (lane gaps 20–40 px, box gaps ≥ 38 px so
   arrows fit), and writes `docs/<section>/img/<name>.svg`. Estimate text width
   as ~7 px per character at 12.5 px sans and ~8.6 px at 14 px mono; size boxes
   from the longest line plus 28 px.
   Done when the file parses (`python3 -c 'import xml.dom.minidom,sys; xml.dom.minidom.parse(sys.argv[1])' <file>`).

3. **Render and look** with [render.mjs](render.mjs) (resvg, fonts bundled, so
   it works without system fonts or a browser):

   ```bash
   W=/var/tmp/svg-render && mkdir -p $W && cd $W
   npm i --no-audit --no-fund @resvg/resvg-js@2 dejavu-fonts-ttf@2
   cp <repo>/.agents/skills/svg-diagrams/render.mjs .
   node render.mjs <repo>/docs/<section>/img/<name>.svg $W/<name>.png
   ```

   View the PNG. Fix and regenerate until no text overlaps a box edge, another
   label or an arrow; arrows start and end on box edges; the legend lists exactly
   the colours used. DejaVu runs wider than Inter, so text that fits here fits in
   the browser.
   Done when the PNG shows none of these defects.

4. **Embed** as `![<alt text that states what the diagram shows>](img/<name>.svg)`
   and keep the `<title>`, `<desc>` and alt text describing the same thing.
   If the diagram replaces a Mermaid block, delete the block.
   Done when `zensical build` (or `mkdocs build`) shows the image and
   `docs/llms.txt` needs no change (images are not pages).

5. **Clean up**: `rm -r $W` and the generator script; commit only the `.svg`
   and the Markdown that embeds it.
   Done when `git status` lists only those files.
