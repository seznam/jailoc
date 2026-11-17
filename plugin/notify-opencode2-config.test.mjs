import { test } from "node:test";
import assert from "node:assert/strict";
import { openCode2BuiltInNotifies, parseJsonc } from "./notify-opencode2.mjs";

function missing(path) {
  const error = new Error(`ENOENT: no such file or directory, open '${path}'`);
  error.code = "ENOENT";
  throw error;
}

// builtIn evaluates the effective OpenCode 2.2.5 CLI config: cli.json in the
// config directory, deep-merged with OPENCODE_CLI_CONFIG_CONTENT.
function builtIn({ files = {}, env = {} } = {}) {
  return openCode2BuiltInNotifies({
    env,
    homedir: () => "/home/agent",
    readFile: (path) => (path in files ? files[path] : missing(path)),
  });
}

const cli = "/home/agent/.config/opencode/cli.json";

test("parseJsonc accepts comments and trailing commas but keeps // inside strings", () => {
  const value = parseJsonc(`{
    // line comment
    "$schema": "https://opencode.ai/v2/cli.json", /* block */
    "list": [1, 2,],
  }`);

  assert.deepEqual(value, { $schema: "https://opencode.ai/v2/cli.json", list: [1, 2] });
});

test("parseJsonc keeps commas and comment markers inside strings", () => {
  assert.deepEqual(parseJsonc(`{"a": "x, }", "b": "/* no */", "c": "say \\"hi\\", ]"}`), {
    a: "x, }",
    b: "/* no */",
    c: 'say "hi", ]',
  });
});

test("parseJsonc returns undefined for invalid text", () => {
  assert.equal(parseJsonc(`{ "attention": `), undefined);
  assert.equal(parseJsonc(`{ /* open`), undefined);
});

test("notifications are off without a cli.json", () => {
  assert.equal(builtIn(), false);
});

test("JSONC cli.json enabling notifications hands notifying to the built-in", () => {
  const files = { [cli]: `{\n  // notify me\n  "attention": { "notifications": true, },\n}\n` };

  assert.equal(builtIn({ files }), true);
});

test("notifications false or non-boolean stay off", () => {
  assert.equal(builtIn({ files: { [cli]: `{"attention":{"notifications":false}}` } }), false);
  assert.equal(builtIn({ files: { [cli]: `{"attention":{"notifications":"yes"}}` } }), false);
});

test("invalid cli.json counts as absent", () => {
  assert.equal(builtIn({ files: { [cli]: `{"attention":{"notifications":true}` } }), false);
});

test("OPENCODE_CLI_CONFIG_CONTENT overrides the file setting", () => {
  const files = { [cli]: `{"attention":{"notifications":true,"sound":true}}` };

  assert.equal(builtIn({ files, env: { OPENCODE_CLI_CONFIG_CONTENT: `{"attention":{"notifications":false}}` } }), false);
  assert.equal(builtIn({ env: { OPENCODE_CLI_CONFIG_CONTENT: `{"attention":{"notifications":true}}` } }), true);
});

test("config directory follows OPENCODE_CONFIG_DIR, then XDG_CONFIG_HOME", () => {
  const on = `{"attention":{"notifications":true}}`;

  assert.equal(builtIn({ files: { "/cfg/cli.json": on }, env: { OPENCODE_CONFIG_DIR: "/cfg" } }), true);
  assert.equal(builtIn({ files: { "/xdg/opencode/cli.json": on }, env: { XDG_CONFIG_HOME: "/xdg" } }), true);
  assert.equal(builtIn({ files: { [cli]: on }, env: { OPENCODE_CONFIG_DIR: "/cfg" } }), false);
});

test("plugin directives that disable opencode.notifications leave notifying to jailoc", () => {
  const files = { [cli]: `{"attention":{"notifications":true},"plugins":["-opencode.notifications"]}` };
  const env = { OPENCODE_CLI_CONFIG_CONTENT: `{"plugins":["-opencode.*","file:///etc/jailoc-tui-plugin"]}` };

  assert.equal(builtIn({ files }), false);
  assert.equal(builtIn({ files: { [cli]: `{"attention":{"notifications":true}}` }, env }), false);
});

test("a later plugin directive re-enables opencode.notifications", () => {
  const files = {
    [cli]: `{"attention":{"notifications":true},"plugins":["-*",{"package":"opencode.notifications"}]}`,
  };

  assert.equal(builtIn({ files }), true);
});
