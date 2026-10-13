import { mkdir, readFile, writeFile } from "node:fs/promises";
import { transformAsync } from "@babel/core";
import solid from "babel-preset-solid";
import ts from "@babel/preset-typescript";

// One build per OpenCode TUI plugin API: opencode1 (OpenCode 1.x) and
// opencode2 (OpenCode 2.x).
const plugins = ["opencode1", "opencode2"];

await mkdir("./dist", { recursive: true });
for (const name of plugins) {
  const source = `./plugin/${name}.jsx`;
  const code = await readFile(source, "utf8");
  const result = await transformAsync(code, {
    filename: source,
    configFile: false,
    babelrc: false,
    presets: [[solid, { moduleName: "@opentui/solid", generate: "universal" }], [ts]],
  });

  if (!result?.code) {
    throw new Error(`failed to build tui plugin ${name}`);
  }

  await writeFile(`./dist/tui-${name}.js`, result.code);
}
