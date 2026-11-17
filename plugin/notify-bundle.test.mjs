import { test } from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { registerHooks } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";

// These tests load the embedded TUI plugin bundles that jailoc ships, so they
// fail when internal/embed/assets falls behind the plugin sources. Only the
// peer modules OpenCode provides at runtime are stubbed.
const peers = {
  "@opentui/solid": `
    export const createElement = (tag) => ({ tag, props: {}, children: [] });
    export const createTextNode = (text) => ({ text });
    export const insertNode = (parent, node) => parent.children.push(node);
    export const insert = (parent, value) => parent.children.push({ text: String(value) });
    export const setProp = (node, name, value) => { node.props[name] = value; return value; };
    export const effect = (fn) => fn(undefined);
  `,
  "@opencode/plugin/tui": `export const Plugin = { define: (plugin) => plugin };`,
};
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (!Object.hasOwn(peers, specifier)) return nextResolve(specifier, context);
    return { url: `data:text/javascript,${encodeURIComponent(peers[specifier])}`, shortCircuit: true };
  },
});

const asset = (name) => new URL(`../internal/embed/assets/tui-${name}.js`, import.meta.url).href;
const completed = "\x1b]777;jailoc-notify;completed\x07";
const input = "\x1b]777;jailoc-notify;input\x07";

const text = (node) => node.text ?? node.children.map(text).join("");

// captureStdout runs fn and returns what it wrote to process.stdout.
function captureStdout(fn) {
  const written = [];
  const write = process.stdout.write;
  process.stdout.write = (chunk) => {
    written.push(String(chunk));
    return true;
  };
  try {
    fn();
  } finally {
    process.stdout.write = write;
  }
  return written;
}

function emitter() {
  const handlers = new Map();
  return {
    on(type, handler) {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type).add(handler);
      return () => handlers.get(type).delete(handler);
    },
    emit(type, event) {
      for (const handler of handlers.get(type) ?? []) handler(event);
    },
    size: () => [...handlers.values()].reduce((count, set) => count + set.size, 0),
  };
}

test("embedded OpenCode 1 plugin renders the sidebar and writes notifications", async (t) => {
  t.after(() => {
    delete process.env.JAILOC;
    delete process.env.JAILOC_WORKSPACE;
  });
  process.env.JAILOC = "1";
  process.env.JAILOC_WORKSPACE = "demo";
  const plugin = (await import(asset("opencode1"))).default;
  const events = emitter();
  const registered = [];
  const disposers = [];
  const sessions = { root: { id: "root" } };
  const api = {
    theme: { current: { text: { secondary: "gray" } } },
    slots: { register: (slot) => registered.push(slot) },
    lifecycle: { onDispose: (fn) => disposers.push(fn) },
    tuiConfig: { attention: { enabled: false, notifications: true } },
    plugins: { list: () => [] },
    state: { session: { get: (id) => sessions[id] } },
    event: { on: events.on },
  };
  const fire = (type, properties) => events.emit(type, { id: "evt", type, properties });
  const status = (type) => fire("session.status", { sessionID: "root", status: { type } });
  const assistant = (fields) =>
    fire("message.updated", {
      sessionID: "root",
      info: { id: "msg_reply", sessionID: "root", role: "assistant", time: { created: 1, completed: 2 }, ...fields },
    });

  plugin.tui(api);

  assert.equal(text(registered[0].slots.sidebar_content()), "🔒 jailoc / demo");
  const written = captureStdout(() => {
    status("busy");
    assistant({ error: { name: "MessageAbortedError", data: { message: "Aborted" } } });
    status("idle");
    status("busy");
    assistant({ finish: "stop" });
    status("idle");
    fire("question.asked", { id: "que_1", sessionID: "root" });
  });
  assert.deepEqual(written, [completed, input]);

  for (const dispose of disposers) dispose();
  assert.equal(events.size(), 0);
});

test("embedded OpenCode 2 plugin renders the sidebar and writes notifications", async (t) => {
  const configDir = mkdtempSync(join(tmpdir(), "jailoc-notify-"));
  t.after(() => {
    rmSync(configDir, { recursive: true, force: true });
    delete process.env.JAILOC;
    delete process.env.JAILOC_WORKSPACE;
    delete process.env.OPENCODE_CONFIG_DIR;
  });
  process.env.JAILOC = "1";
  process.env.JAILOC_WORKSPACE = "demo";
  process.env.OPENCODE_CONFIG_DIR = configDir;
  delete process.env.OPENCODE_CLI_CONFIG_CONTENT;
  const plugin = (await import(asset("opencode2"))).default;
  const events = emitter();
  const slots = [];
  const sessions = { root: { id: "root" } };
  const running = new Set();
  const context = {
    theme: { text: { muted: "gray" } },
    ui: {
      slot: (slot) => {
        slots.push(slot);
        return () => slots.splice(slots.indexOf(slot), 1);
      },
    },
    data: {
      on: events.on,
      session: {
        get: (id) => sessions[id],
        family: (id) => [id],
        status: (id) => (running.has(id) ? "running" : "idle"),
      },
    },
  };
  const fire = (type, data) => {
    if (type === "session.execution.started") running.add(data.sessionID);
    else running.delete(data.sessionID);
    events.emit(type, { id: "evt", type, data });
  };

  const dispose = plugin.setup(context);

  assert.equal(slots[0].append, "sidebar.content");
  assert.equal(text(slots[0].render()), "🔒 jailoc / demo");
  const written = captureStdout(() => {
    fire("session.execution.started", { sessionID: "root" });
    fire("session.execution.interrupted", { sessionID: "root", reason: "user" });
    fire("session.execution.started", { sessionID: "root" });
    fire("session.execution.succeeded", { sessionID: "root" });
    fire("permission.asked", { id: "per_1", sessionID: "root", action: "bash", resources: [] });
  });
  assert.deepEqual(written, [completed, input]);

  dispose();
  assert.equal(events.size(), 0);
  assert.equal(slots.length, 0);
});
