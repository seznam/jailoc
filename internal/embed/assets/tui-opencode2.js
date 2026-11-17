import { effect as _$effect } from "@opentui/solid";
import { insert as _$insert } from "@opentui/solid";
import { createTextNode as _$createTextNode } from "@opentui/solid";
import { insertNode as _$insertNode } from "@opentui/solid";
import { setProp as _$setProp } from "@opentui/solid";
import { createElement as _$createElement } from "@opentui/solid";
/** @jsxImportSource @opentui/solid */
import { Plugin } from "@opencode/plugin/tui";
// Inlined from plugin/notify-core.mjs.
// Shared, non-visual helpers for the jailoc notification producers. The build
// inlines this module into each TUI plugin, so it must stay self-contained.

// The private jailoc OSC 777 subcommand. The jailoc host consumes these
// sequences from the session output; they are not desktop notifications.
const notificationSequences = {
  completed: "\x1b]777;jailoc-notify;completed\x07",
  input: "\x1b]777;jailoc-notify;input\x07"
};

// Upper bound for every per-session or per-request set a producer keeps.
const notifyStateLimit = 256;

// writeNotification sends one jailoc notification of kind "completed" or
// "input". The TUI renderers leave process.stdout in passthrough mode, so the
// sequence reaches the session output unchanged.
function writeNotification(kind, write = chunk => process.stdout.write(chunk)) {
  const sequence = Object.hasOwn(notificationSequences, kind) ? notificationSequences[kind] : undefined;
  if (sequence === undefined) throw new Error(`unknown jailoc notification kind: ${kind}`);
  write(sequence);
}

// boundedSet returns a set that forgets its oldest entries beyond limit, so
// state for sessions or requests that never settle cannot grow without end.
function boundedSet(limit = notifyStateLimit) {
  const values = new Set();
  return {
    add(value) {
      values.delete(value);
      values.add(value);
      if (values.size > limit) values.delete(values.values().next().value);
    },
    has: value => values.has(value),
    delete: value => values.delete(value),
    clear: () => values.clear(),
    [Symbol.iterator]: () => values.values()
  };
}

// Inlined from plugin/notify-opencode2.mjs.
// jailoc notification producer for the OpenCode 2.2.5 TUI plugin API. The
// build inlines this module into the OpenCode 2 plugin.
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

// The built-in plugin that raises OpenCode 2's own attention notifications.
const openCode2NotificationsPlugin = "opencode.notifications";

// How long a root completion that waited on its subagents waits for the root
// to be woken by their reports before it is emitted anyway.
const openCode2SettleMs = 3000;

// parseJsonc parses the JSONC that OpenCode 2 accepts in cli.json (comments
// and trailing commas). It returns undefined for invalid text, as OpenCode
// treats an unparsable config as absent.
function parseJsonc(text) {
  let json = "";
  let index = text.charCodeAt(0) === 0xfeff ? 1 : 0;
  while (index < text.length) {
    const char = text[index];
    if (char === '"') {
      const end = jsoncStringEnd(text, index);
      if (end === -1) return undefined;
      json += text.slice(index, end);
      index = end;
    } else if (text.startsWith("//", index)) {
      const end = text.indexOf("\n", index);
      index = end === -1 ? text.length : end;
    } else if (text.startsWith("/*", index)) {
      const end = text.indexOf("*/", index + 2);
      if (end === -1) return undefined;
      json += " ";
      index = end + 2;
    } else {
      // Drop a trailing comma before a closing bracket. Strings are copied
      // whole and end in a quote, so a comma at the end of json is never
      // inside a string.
      if (char === "}" || char === "]") {
        json = json.trimEnd();
        if (json.endsWith(",")) json = json.slice(0, -1);
      }
      json += char;
      index += 1;
    }
  }
  try {
    return JSON.parse(json);
  } catch (error) {
    if (error instanceof SyntaxError) return undefined;
    throw error;
  }
}

// jsoncStringEnd returns the index after the string literal starting at
// start, or -1 when it is unterminated.
function jsoncStringEnd(text, start) {
  for (let index = start + 1; index < text.length; index += 1) {
    if (text[index] === "\\") index += 1;else if (text[index] === '"') return index + 1;else if (text[index] === "\n") return -1;
  }
  return -1;
}
function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

// mergeRecords deep-merges overlay into base like OpenCode 2's CLI config
// loader: nested objects merge, every other value replaces.
function mergeRecords(base, overlay) {
  const result = {
    ...base
  };
  for (const [key, value] of Object.entries(overlay)) {
    result[key] = isRecord(result[key]) && isRecord(value) ? mergeRecords(result[key], value) : value;
  }
  return result;
}
function openCode2ConfigRecord(text) {
  if (text === undefined) return {};
  const value = parseJsonc(text);
  return isRecord(value) ? value : {};
}

// openCode2PluginEnabled applies OpenCode 2's ordered plugin directives to a
// built-in plugin, which starts enabled: "-selector" disables it, a matching
// selector enables it again.
function openCode2PluginEnabled(plugins, id) {
  const matches = selector => selector === "*" || selector === id || selector.endsWith(".*") && id.startsWith(selector.slice(0, -1));
  let enabled = true;
  for (const entry of Array.isArray(plugins) ? plugins : []) {
    const target = typeof entry === "string" ? entry : entry?.package;
    if (typeof target !== "string") continue;
    if (target.startsWith("-")) {
      if (matches(target.slice(1))) enabled = false;
    } else if (matches(target)) {
      enabled = true;
    }
  }
  return enabled;
}

// openCode2BuiltInNotifies reports whether OpenCode 2 raises its own system
// notifications. It evaluates the effective CLI config the way OpenCode 2.2.5
// loads it: cli.json in OPENCODE_CONFIG_DIR, else $XDG_CONFIG_HOME/opencode,
// else ~/.config/opencode, deep-merged with OPENCODE_CLI_CONFIG_CONTENT; then
// attention.notifications must be true and the opencode.notifications plugin
// enabled by the plugin directives.
function openCode2BuiltInNotifies({
  env = process.env,
  readFile = path => readFileSync(path, "utf8"),
  homedir: home = homedir
} = {}) {
  const dir = env.OPENCODE_CONFIG_DIR || join(env.XDG_CONFIG_HOME || join(home(), ".config"), "opencode");
  let text;
  try {
    text = readFile(join(dir, "cli.json"));
  } catch {
    // OpenCode 2 reads an unreadable cli.json as no file (orElseSucceed), so
    // the effective config has no file settings either.
    text = undefined;
  }
  const config = mergeRecords(openCode2ConfigRecord(text), openCode2ConfigRecord(env.OPENCODE_CLI_CONFIG_CONTENT));
  if (!isRecord(config.attention) || config.attention.notifications !== true) return false;
  return openCode2PluginEnabled(config.plugins, openCode2NotificationsPlugin);
}

// openCode2IsRoot reports whether sessionID is a known top-level session.
// Unknown sessions are not roots, so they never notify.
function openCode2IsRoot(data, sessionID) {
  const session = data.session.get(sessionID);
  return session !== undefined && session.parentID == null;
}

// openCode2HasRunningChild mirrors the built-in notifications plugin: only
// direct children count, as a grandchild reports to its own parent.
function openCode2HasRunningChild(data, sessionID) {
  return data.session.family(sessionID).some(child => child !== sessionID && data.session.get(child)?.parentID === sessionID && data.session.status(child) === "running");
}

// watchOpenCode2 calls host.emit("completed") when a root session's execution
// succeeds and host.emit("input") when a root session asks a permission or
// creates a form, unless host.builtInNotifies() reports that OpenCode notifies
// itself. A root that succeeds while direct subagents still run is pending
// until it runs again (and its next success emits) or, when no new execution
// of the root or a direct subagent starts within openCode2SettleMs of the last
// subagent ending, emits then. It returns a function that unsubscribes the
// handlers and cancels timers.
function watchOpenCode2(context, host) {
  const data = context.data;
  const timers = host.timers ?? {
    set: (fn, ms) => setTimeout(fn, ms),
    clear: handle => clearTimeout(handle)
  };
  const requests = boundedSet();
  const waiting = boundedSet();
  const settling = new Map();
  const notify = kind => {
    if (!host.builtInNotifies()) host.emit(kind);
  };
  // hold cancels a pending root's settle timer but keeps it pending.
  const hold = sessionID => {
    if (!settling.has(sessionID)) return;
    timers.clear(settling.get(sessionID));
    settling.delete(sessionID);
  };
  const drop = sessionID => {
    waiting.delete(sessionID);
    hold(sessionID);
  };
  // settled runs when a settle timer fires. The callback may already be
  // queued when its timer is cleared, so it checks the root again.
  const settled = (sessionID, handle) => {
    if (settling.get(sessionID) === handle) settling.delete(sessionID);
    if (!waiting.has(sessionID)) return;
    if (!openCode2IsRoot(data, sessionID)) {
      waiting.delete(sessionID);
      return;
    }
    if (data.session.status(sessionID) !== "idle" || openCode2HasRunningChild(data, sessionID)) return;
    drop(sessionID);
    notify("completed");
  };
  const settle = () => {
    for (const sessionID of waiting) {
      if (settling.has(sessionID) || openCode2HasRunningChild(data, sessionID)) continue;
      const handle = timers.set(() => settled(sessionID, handle), openCode2SettleMs);
      settling.set(sessionID, handle);
    }
  };
  const ended = sessionID => {
    if (openCode2IsRoot(data, sessionID)) drop(sessionID);else settle();
  };
  const asked = (id, sessionID) => {
    if (requests.has(id)) return;
    requests.add(id);
    if (openCode2IsRoot(data, sessionID)) notify("input");
  };
  const unsubscribe = [data.on("session.execution.started", event => {
    const sessionID = event.data.sessionID;
    drop(sessionID);
    // A direct subagent starting means the pending root is not settled yet.
    const parentID = data.session.get(sessionID)?.parentID;
    if (parentID != null) hold(parentID);
  }), data.on("session.execution.succeeded", event => {
    const sessionID = event.data.sessionID;
    if (!openCode2IsRoot(data, sessionID)) {
      settle();
      return;
    }
    drop(sessionID);
    if (openCode2HasRunningChild(data, sessionID)) {
      waiting.add(sessionID);
      return;
    }
    notify("completed");
  }), data.on("session.execution.failed", event => ended(event.data.sessionID)), data.on("session.execution.interrupted", event => ended(event.data.sessionID)), data.on("permission.asked", event => asked(event.data.id, event.data.sessionID)), data.on("form.created", event => asked(event.data.form.id, event.data.form.sessionID)), data.on("permission.replied", event => requests.delete(event.data.requestID)), data.on("form.replied", event => requests.delete(event.data.id)), data.on("form.cancelled", event => requests.delete(event.data.id))];
  return () => {
    for (const stop of unsubscribe) stop();
    for (const handle of settling.values()) timers.clear(handle);
    settling.clear();
    waiting.clear();
    requests.clear();
  };
}
export default Plugin.define({
  id: "jailoc",
  setup(context) {
    if (!process.env.JAILOC) return;
    const workspace = process.env.JAILOC_WORKSPACE || "unknown";
    const removeSlot = context.ui.slot({
      append: "sidebar.content",
      render: () => (() => {
        var _el$ = _$createElement("box"),
          _el$2 = _$createElement("text"),
          _el$3 = _$createTextNode(`🔒 jailoc / `);
        _$insertNode(_el$, _el$2);
        _$setProp(_el$, "flexDirection", "column");
        _$insertNode(_el$2, _el$3);
        _$insert(_el$2, workspace, null);
        _$effect(_$p => _$setProp(_el$2, "fg", context.theme.text.muted, _$p));
        return _el$;
      })()
    });
    const stopNotifications = watchOpenCode2(context, {
      emit: kind => writeNotification(kind),
      builtInNotifies: () => openCode2BuiltInNotifies()
    });
    return () => {
      stopNotifications();
      removeSlot();
    };
  }
});