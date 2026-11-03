// Renders an SVG diagram to PNG with resvg and the bundled DejaVu fonts, so the
// result does not depend on system fonts or a browser.
//
// Usage (from a directory with @resvg/resvg-js and dejavu-fonts-ttf installed):
//   node render.mjs <input.svg> <output.png> [scale]
import { readFileSync, readdirSync, writeFileSync } from "node:fs";
import { createRequire } from "node:module";
import { join } from "node:path";

const require = createRequire(join(process.cwd(), "noop.js"));
const { Resvg } = require("@resvg/resvg-js");

const [input, output, scale = "2"] = process.argv.slice(2);
if (!input || !output) {
  console.error("usage: node render.mjs <input.svg> <output.png> [scale]");
  process.exit(2);
}

const fontDir = join(process.cwd(), "node_modules", "dejavu-fonts-ttf", "ttf");
const fontFiles = readdirSync(fontDir)
  .filter((name) => name.endsWith(".ttf"))
  .map((name) => join(fontDir, name));

const svg = readFileSync(input, "utf8");
const width = Number(/<svg[^>]*\swidth="(\d+(?:\.\d+)?)"/.exec(svg)?.[1] ?? 1200);
const resvg = new Resvg(svg, {
  background: "#FFFFFF",
  fitTo: { mode: "width", value: Math.round(width * Number(scale)) },
  font: { fontFiles, loadSystemFonts: false, defaultFontFamily: "DejaVu Sans" },
});
writeFileSync(output, resvg.render().asPng());
console.log(`${output}: ${resvg.width}x${resvg.height} source`);
