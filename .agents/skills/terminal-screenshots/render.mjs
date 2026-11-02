// Renders ANSI terminal transcripts as macOS-style dark terminal window PNGs.
// Usage: node render.mjs <in-dir-with-*.ansi> <out-dir>
// Transcript lines starting with "$ " are prompts; the first one titles the
// window. A transcript without a trailing newline ends with the cursor on its
// last line (e.g. inside `jailoc shell`). Needs `playwright` resolvable from
// the current directory and a Chromium under $PLAYWRIGHT_BROWSERS_PATH.
import { chromium } from 'playwright';
import { readFileSync, readdirSync, mkdirSync } from 'node:fs';
import { join, basename } from 'node:path';

const CWD = '~/shop';

const PALETTE = { 31: '#ff6b6b', 32: '#5af78e', 34: '#6fa8ff', 36: '#4fc3e8' };

const esc = (s) => s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;');

// Minimal SGR → HTML: bold (1), faint (2), reset (0/22/39), fg 31/32/34/36.
function ansiToHtml(text) {
  let out = '';
  let st = { bold: false, faint: false, fg: null };
  const span = (chunk) => {
    if (!chunk) return;
    const css = [];
    if (st.bold) css.push('font-weight:700');
    if (st.faint) css.push('opacity:.5');
    if (st.fg) css.push(`color:${PALETTE[st.fg]}`);
    out += css.length ? `<span style="${css.join(';')}">${esc(chunk)}</span>` : esc(chunk);
  };
  const re = /\x1b\[([0-9;]*)m/g;
  let last = 0;
  for (let m; (m = re.exec(text)); last = re.lastIndex) {
    span(text.slice(last, m.index));
    for (const code of (m[1] || '0').split(';').map(Number)) {
      if (code === 0) st = { bold: false, faint: false, fg: null };
      else if (code === 1) st.bold = true;
      else if (code === 2) st.faint = true;
      else if (code === 22) st.bold = st.faint = false;
      else if (code === 39) st.fg = null;
      else if (PALETTE[code]) st.fg = code;
    }
  }
  span(text.slice(last));
  return out;
}

function page(transcript) {
  const open = !transcript.endsWith('\n');
  const lines = transcript.replace(/\n$/, '').split('\n');
  const cmds = lines.filter((l) => l.startsWith('$ ')).map((l) => l.slice(2));
  const body = lines
    .map((l) => (l.startsWith('$ ') ? `<span class="p">$</span> <span class="c">${esc(l.slice(2))}</span>` : ansiToHtml(l)))
    .join('\n');
  // An open last line (a shell prompt) or a follow (-f) command still running:
  // no new prompt, only the cursor.
  const last = cmds.at(-1) ?? '';
  const cur = '<span class="cur">█</span>';
  const end = open ? cur : '\n' + (last.endsWith(' -f') ? '' : '<span class="p">$</span> ') + cur;
  return `<!doctype html><html><head><meta charset="utf-8">
<link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=block" rel="stylesheet">
<style>
  body { margin: 0; background: transparent; display: inline-block; }
  .win { background: #1e1f29; border-radius: 10px; overflow: hidden; border: 1px solid #3a3b47; }
  .bar { height: 34px; background: #2a2b37; display: flex; align-items: center; padding: 0 14px;
         position: relative; font: 13px 'JetBrains Mono', ui-monospace, monospace; color: #8a8c9d; }
  .dot { width: 12px; height: 12px; border-radius: 50%; margin-right: 8px; }
  .title { position: absolute; left: 0; right: 0; text-align: center; pointer-events: none; }
  pre { margin: 0; padding: 18px 22px 22px; color: #e6e6e6;
        font: 13px/1.55 'JetBrains Mono', ui-monospace, monospace; white-space: pre; }
  .p { color: #f5a623; } .c { font-weight: 700; color: #fff; } .cur { color: #d6d6e6; }
</style></head><body><div class="win">
<div class="bar"><span class="dot" style="background:#ff5f57"></span><span class="dot" style="background:#febc2e"></span><span class="dot" style="background:#28c840"></span>
<span class="title">${esc(CWD)} — ${esc(cmds[0] ?? '')}</span></div>
<pre>${body}${end}</pre></div></body></html>`;
}

const [inDir, outDir] = process.argv.slice(2);
if (!inDir || !outDir) {
  console.error('usage: node render.mjs <in-dir> <out-dir>');
  process.exit(2);
}
mkdirSync(outDir, { recursive: true });

const browser = await chromium.launch();
const ctx = await browser.newContext({ deviceScaleFactor: 2, viewport: { width: 1600, height: 900 } });
const p = await ctx.newPage();
for (const file of readdirSync(inDir).filter((f) => f.endsWith('.ansi'))) {
  const name = basename(file, '.ansi');
  await p.setContent(page(readFileSync(join(inDir, file), 'utf8')), { waitUntil: 'networkidle' });
  await p.evaluate(() => document.fonts.ready);
  const out = join(outDir, `${name}.png`);
  await p.locator('.win').screenshot({ path: out, omitBackground: true });
  console.log(out);
}
await browser.close();
