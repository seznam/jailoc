import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, readdirSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { openCode2BuiltInNotifies, parseJsonc } from "./notify-opencode2.mjs";

function missing(path) {
  const error = new Error(`ENOENT: no such file or directory, open '${path}'`);
  error.code = "ENOENT";
  throw error;
}

function denied(path) {
  const error = new Error(`EACCES: permission denied, open '${path}'`);
  error.code = "EACCES";
  return error;
}

// builtIn evaluates the effective OpenCode 2.2.5 CLI config: cli.json in the
// config directory (or the tui.json settings OpenCode migrates when there is
// no cli.json), deep-merged with OPENCODE_CLI_CONFIG_CONTENT. A file whose
// value is an Error fails to read with that error.
function builtIn({ files = {}, env = {} } = {}) {
  return openCode2BuiltInNotifies({
    env,
    homedir: () => "/home/agent",
    readFile: (path) => {
      if (!(path in files)) missing(path);
      if (files[path] instanceof Error) throw files[path];
      return files[path];
    },
  });
}

const cli = "/home/agent/.config/opencode/cli.json";
const tui = "/home/agent/.config/opencode/tui.json";

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

test("without a cli.json, tui.json attention.notifications true hands notifying to the built-in", () => {
  assert.equal(builtIn({ files: { [tui]: `{\n  // legacy\n  "attention": { "notifications": true, },\n}` } }), true);
});

test("tui.json without attention.notifications true stays off, as OpenCode 2 defaults it to false", () => {
  assert.equal(builtIn({ files: { [tui]: `{"theme":"x"}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{"attention":{"enabled":true,"sound":true}}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{"attention":{"notifications":false}}` } }), false);
});

test("tui.json attention.enabled false does not disable migrated notifications", () => {
  assert.equal(builtIn({ files: { [tui]: `{"attention":{"enabled":false,"notifications":true}}` } }), true);
});

test("an existing cli.json wins over tui.json, even when it is invalid or unreadable", () => {
  const legacy = `{"attention":{"notifications":true}}`;

  assert.equal(builtIn({ files: { [cli]: `{}`, [tui]: legacy } }), false);
  assert.equal(builtIn({ files: { [cli]: `{"attention":`, [tui]: legacy } }), false);
  assert.equal(builtIn({ files: { [cli]: denied(cli), [tui]: legacy } }), false);
  const env = { OPENCODE_CLI_CONFIG_CONTENT: legacy };
  assert.equal(builtIn({ files: { [cli]: denied(cli), [tui]: legacy }, env }), true);
});

test("invalid tui.json settings are not migrated", () => {
  assert.equal(builtIn({ files: { [tui]: `{"attention":{"notifications":true}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `[{"attention":{"notifications":true}}]` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{"attention":{"notifications":"yes"}}` } }), false);
  // OpenCode rejects the whole tui.json when a plugin field has the wrong type.
  const on = `"attention":{"notifications":true}`;
  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin_enabled":{"other":"yes"}}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin":[1]}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin":[["other"]]}` } }), false);
  assert.equal(builtIn({ files: { [tui]: denied(tui) } }), false);
});

test("tui.json plugin then plugin_enabled directives apply in order", () => {
  const on = `"attention":{"notifications":true}`;

  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin_enabled":{"opencode.notifications":false}}` } }), false);
  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin":["-opencode.*"]}` } }), false);
  assert.equal(
    builtIn({ files: { [tui]: `{${on},"plugin":["-opencode.*"],"plugin_enabled":{"opencode.notifications":true}}` } }),
    true,
  );
  assert.equal(builtIn({ files: { [tui]: `{${on},"plugin":["-*",["opencode.notifications",{}]]}` } }), true);
});

test("OPENCODE_CLI_CONFIG_CONTENT overrides migrated tui.json settings and replaces its plugins", () => {
  const files = { [tui]: `{"attention":{"notifications":true},"plugin_enabled":{"opencode.notifications":false}}` };

  assert.equal(builtIn({ files }), false);
  const env = { OPENCODE_CLI_CONFIG_CONTENT: `{"plugins":["file:///etc/jailoc-tui-plugin"]}` };
  assert.equal(builtIn({ files, env }), true);
  assert.equal(
    builtIn({
      files: { [tui]: `{"attention":{"notifications":true}}` },
      env: { OPENCODE_CLI_CONFIG_CONTENT: `{"attention":{"notifications":false}}` },
    }),
    false,
  );
});

test("tui.json is read from the same config directory as cli.json", () => {
  const on = `{"attention":{"notifications":true}}`;

  assert.equal(builtIn({ files: { "/cfg/tui.json": on }, env: { OPENCODE_CONFIG_DIR: "/cfg" } }), true);
  assert.equal(builtIn({ files: { "/xdg/opencode/tui.json": on }, env: { XDG_CONFIG_HOME: "/xdg" } }), true);
  assert.equal(builtIn({ files: { [tui]: on }, env: { OPENCODE_CONFIG_DIR: "/cfg" } }), false);
});

test("reading a real config directory leaves it unchanged", (t) => {
  const dir = mkdtempSync(join(tmpdir(), "jailoc-oc2-config-"));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const legacy = join(dir, "tui.json");
  writeFileSync(legacy, `{"attention":{"notifications":true}}\n`);
  const before = statSync(legacy).mtimeMs;

  assert.equal(openCode2BuiltInNotifies({ env: { OPENCODE_CONFIG_DIR: dir } }), true);
  assert.deepEqual(readdirSync(dir), ["tui.json"]);
  assert.equal(readFileSync(legacy, "utf8"), `{"attention":{"notifications":true}}\n`);
  assert.equal(statSync(legacy).mtimeMs, before);

  writeFileSync(join(dir, "cli.json"), `{"theme":{"name":"x"}}`);
  assert.equal(openCode2BuiltInNotifies({ env: { OPENCODE_CONFIG_DIR: dir } }), false);
  assert.deepEqual(readdirSync(dir).sort(), ["cli.json", "tui.json"]);
});
