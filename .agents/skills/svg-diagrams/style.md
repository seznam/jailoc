# Diagram style

Values copied from [architecture.svg](../../../docs/explanation/img/architecture.svg),
which itself follows the diagrams of the SCIF demo-tracing project. Reuse them
exactly; a diagram that invents a colour or size reads as foreign next to the
others.

## Palette

| Role | Accent | Lane fill | Meaning |
|---|---|---|---|
| jailoc on the host | `#D24E00` | `#FFF1E6` | jailoc CLI, supervisor, broker, caches |
| jailoc in the VM | `#2E7D32` | `#EEF8EC` | jailoc-guest and what it sets up |
| agent | `#8250DF` | — | coding agent and the processes it starts |
| third party | `#425CC7` | — | libkrun, kernel, virtio devices, boundaries |
| external | `#9AA6B8` | — | Internet, host files, anything jailoc does not own |

Text: title `#24292E`, body `#464C54`, subtitle and arrow labels `#6E7180`.
Borders `#DCE0E5`. Chips fill `#EEF1FB`, stroke `#C9D1F0`.

Fonts: root `font-family="Inter,Helvetica,Arial,sans-serif"`; component names
and chips `font-family="JetBrains Mono,Menlo,monospace"`.

## Elements

**Root** — `<svg xmlns="http://www.w3.org/2000/svg" width W height H viewBox="0 0 W H" font-family=… role="img" aria-labelledby="t d">`,
then `<title id="t">` (short name) and `<desc id="d">` (one-sentence story).
Width 1200 fits the docs column.

**Arrow markers** in `<defs>`: `ag` gray, `ab` blue —
`<marker id="ag" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0 0L10 5L0 10z" fill="#9AA6B8"/></marker>`.

**Card** — `<rect width=W height=H fill="#FFFFFF" stroke="#DCE0E5" rx="8"/>`;
title at `x=16 y=27`, 16 px, weight 700.

**Legend** — top right, laid out right to left from `x=W-16`: label
(`text-anchor="end"`, 12 px, weight 700, accent colour), then a 9×9 `rx=2`
square in the same colour ending 8 px left of the label, 17 px gap to the next
label. Only colours the diagram uses.

**Lane** — tinted rect `x=8`, full width minus 16, `rx=6`; a 6 px accent bar
`rx=3` on its left edge; label at `x=26`, `y=top+17`, uppercase, 12 px,
weight 800, `letter-spacing="0.08em"`, accent colour. First box row starts
40 px below the lane top.

**Box** — white rect `rx=6`, stroke `#DCE0E5` (add `stroke-dasharray="5 4"`
for external things); 3 px accent bar `rx=1.5` inset 10 px from top and
bottom at the left edge; name at `x+14, y+22` (mono, 14 px, weight 700,
accent); subtitle at `y+40` (12 px, `#6E7180`); body lines from `y+60`, every
19 px (12.5 px, `#464C54`). Height 80 for name + subtitle + one line, +19 per
extra line.

**Chip** — inside a box at `y+58`: rect height 20, `rx=4`, width
`len(text)*7 + 10`, 8 px apart; text centred, mono 11.5 px, weight 600, blue.

**Footer strip** — a full-width box of height 40 with one line:
`<tspan>` mono name in accent + `<tspan>` body text, 13 px.

**Arrows** — straight `M x1 y L x2 y` between box edges, 2 px short of the
target so the marker touches it: gray `stroke-width="1.6"` with `ag` inside a
lane; blue `stroke-width="1.8"` with `ab` when crossing a boundary. Labels 11 px
(`#6E7180`, or blue weight 600 for crossing arrows), 8 px beside the line.
Use a cubic curve (`C`) only to route around boxes.

**Side channel** — dashed blue curve (`stroke-dasharray="6 4"`, 1.6) with a
label pill on it: white rect height 18 `rx=9` with blue stroke, text 11 px
weight 600 blue centred.

**Boundary** — a line across the card between lanes, `stroke="#425CC7"`
width 2, `stroke-dasharray="7 5"`, with a filled blue pill (height 20, `rx=10`)
holding white 12 px weight 700 text that names what may cross it.
