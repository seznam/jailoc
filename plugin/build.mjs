import { mkdir, readFile, writeFile } from "node:fs/promises";
import { dirname, join, relative } from "node:path";
import { transformAsync } from "@babel/core";
import solid from "babel-preset-solid";
import ts from "@babel/preset-typescript";

// One build per OpenCode TUI plugin API: opencode1 (OpenCode 1.x) and
// opencode2 (OpenCode 2.x).
const plugins = ["opencode1", "opencode2"];

// Babel does not bundle, and the guest gets each plugin as one tui.js file,
// so relative imports of the plugin's helper modules (plugin/*.mjs) are
// inlined here. Helpers are plain modules with named exports only.
const relativeImport = /^import\s*\{([^}]*)\}\s*from\s*"(\.{1,2}\/[^"]+)";?[ \t]*\n?/gm;

function importedNames(list) {
  return list
    .split(",")
    .map((name) => name.trim())
    .filter(Boolean);
}

// collectHelpers appends the helpers file imports to ordered, dependencies
// first, each helper once.
async function collectHelpers(file, code, ordered, seen) {
  for (const [, names, spec] of code.matchAll(relativeImport)) {
    const helper = join(dirname(file), spec);
    if (!seen.has(helper)) {
      seen.add(helper);
      const source = await readFile(helper, "utf8");
      await collectHelpers(helper, source, ordered, seen);
      ordered.push({ file: helper, source });
    }
    const declared = ordered.find((item) => item.file === helper).source;
    for (const name of importedNames(names)) {
      if (!new RegExp(`^export (?:async )?(?:function|const|let|class) ${name}\\b`, "m").test(declared)) {
        throw new Error(`${relative(".", file)}: ${relative(".", helper)} does not export ${name}`);
      }
    }
  }
}

function helperBody({ file, source }) {
  const body = source.replace(relativeImport, "").replace(/^export (?=(?:async )?(?:function|const|let|class) )/gm, "");
  if (/^export\b/m.test(body)) throw new Error(`${relative(".", file)}: only named function, const, let or class exports can be inlined`);
  return `// Inlined from ${relative(".", file)}.\n${body}`;
}

async function inlineHelpers(file, code) {
  const ordered = [];
  await collectHelpers(file, code, ordered, new Set());
  if (!ordered.length) return code;
  const helpers = ordered.map(helperBody).join("\n");
  let inserted = false;
  return code.replace(relativeImport, () => {
    if (inserted) return "";
    inserted = true;
    return `${helpers}\n`;
  });
}

await mkdir("./dist", { recursive: true });
for (const name of plugins) {
  const source = `./plugin/${name}.jsx`;
  const code = await inlineHelpers(source, await readFile(source, "utf8"));
  const result = await transformAsync(code, {
    filename: source,
    configFile: false,
    babelrc: false,
    presets: [[solid, { moduleName: "@opentui/solid", generate: "universal" }], [ts]],
  });

  if (!result?.code) {
    throw new Error(`failed to build tui plugin ${name}`);
  }
  if (/\bfrom\s*["']\.{1,2}\/|\bimport\s*\(\s*["']\.{1,2}\//.test(result.code)) {
    throw new Error(`tui plugin ${name} still has a relative import`);
  }

  await writeFile(`./dist/tui-${name}.js`, result.code);
}
