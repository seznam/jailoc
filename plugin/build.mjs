import { mkdir, readFile, writeFile } from "node:fs/promises";
import { transformAsync } from "@babel/core";
import solid from "babel-preset-solid";
import ts from "@babel/preset-typescript";

const source = "./plugin/index.jsx";
const target = "./dist/tui.js";

const code = await readFile(source, "utf8");
const result = await transformAsync(code, {
  filename: source,
  configFile: false,
  babelrc: false,
  presets: [[solid, { moduleName: "@opentui/solid", generate: "universal" }], [ts]],
});

if (!result?.code) {
  throw new Error("failed to build tui plugin");
}

await mkdir("./dist", { recursive: true });
await writeFile(target, result.code);
