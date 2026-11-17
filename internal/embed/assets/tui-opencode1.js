import { effect as _$effect } from "@opentui/solid";
import { insert as _$insert } from "@opentui/solid";
import { createTextNode as _$createTextNode } from "@opentui/solid";
import { insertNode as _$insertNode } from "@opentui/solid";
import { setProp as _$setProp } from "@opentui/solid";
import { createElement as _$createElement } from "@opentui/solid";
/** @jsxImportSource @opentui/solid */
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

// Inlined from plugin/notify-opencode1.mjs.
// jailoc notification producer for the OpenCode 1.19.5 TUI plugin API. The
// build inlines this module into the OpenCode 1 plugin.

// The built-in plugin that raises OpenCode 1's own attention notifications.
const openCode1NotificationsPlugin = "internal:notifications";

// openCode1BuiltInNotifies reports whether OpenCode 1 raises its own system
// notifications: attention and its notifications enabled in the resolved TUI
// config, and the built-in notifications plugin not deactivated.
function openCode1BuiltInNotifies(api) {
  const attention = api.tuiConfig.attention;
  if (!attention.enabled || !attention.notifications) return false;
  const status = api.plugins.list().find(plugin => plugin.id === openCode1NotificationsPlugin);
  return status?.active !== false;
}

// openCode1IsRoot reports whether sessionID is a synced top-level session.
// Unknown sessions are not roots, so they never notify.
function openCode1IsRoot(api, sessionID) {
  const session = api.state.session.get(sessionID);
  return session !== undefined && session.parentID == null;
}

// Assistant finish reasons after which OpenCode 1.19.5's prompt loop runs
// another model step instead of ending the turn.
const openCode1StepFinishes = new Set(["tool-calls", "unknown", "error"]);

// openCode1Replied reports whether an assistant message update is a model
// reply that finished normally. An interrupted reply carries an error (an
// abort before the stream started gets one without session.error), and a
// shell command's message only gets time.completed, so neither counts.
function openCode1Replied(info) {
  return info.role === "assistant" && info.error == null && typeof info.finish === "string" && !openCode1StepFinishes.has(info.finish);
}

// watchOpenCode1 calls emit("completed") when a root session's turn ends
// (busy or retry, then idle, without session.error) and the turn's last
// assistant message update is a finished reply, and emit("input") when a
// root session asks a permission or a question. It returns a function that
// unsubscribes the handlers and drops the state.
function watchOpenCode1(api, emit) {
  const active = boundedSet();
  const errored = boundedSet();
  const replied = boundedSet();
  const requests = boundedSet();
  const notify = (kind, sessionID) => {
    if (!openCode1IsRoot(api, sessionID)) return;
    if (openCode1BuiltInNotifies(api)) return;
    emit(kind);
  };
  const asked = event => {
    if (requests.has(event.properties.id)) return;
    requests.add(event.properties.id);
    notify("input", event.properties.sessionID);
  };
  const settled = event => requests.delete(event.properties.requestID);
  const unsubscribe = [api.event.on("session.status", event => {
    const {
      sessionID,
      status
    } = event.properties;
    if (status.type === "busy" || status.type === "retry") {
      // The prompt loop reports busy before every step, so only a new turn
      // forgets the reply of the previous one.
      if (!active.has(sessionID)) replied.delete(sessionID);
      active.add(sessionID);
      errored.delete(sessionID);
      return;
    }
    if (status.type !== "idle" || !active.has(sessionID)) return;
    active.delete(sessionID);
    const finished = replied.has(sessionID) && !errored.has(sessionID);
    replied.delete(sessionID);
    errored.delete(sessionID);
    if (finished) notify("completed", sessionID);
  }), api.event.on("message.updated", event => {
    const info = event.properties.info;
    if (info.role !== "assistant" || !active.has(info.sessionID)) return;
    if (openCode1Replied(info)) replied.add(info.sessionID);else replied.delete(info.sessionID);
  }), api.event.on("session.error", event => {
    const sessionID = event.properties.sessionID;
    if (sessionID !== undefined && active.has(sessionID)) errored.add(sessionID);
  }), api.event.on("permission.asked", asked), api.event.on("question.asked", asked), api.event.on("permission.replied", settled), api.event.on("question.replied", settled), api.event.on("question.rejected", settled)];
  return () => {
    for (const stop of unsubscribe) stop();
    active.clear();
    errored.clear();
    replied.clear();
    requests.clear();
  };
}
export default {
  id: "jailoc",
  tui: api => {
    if (!process.env.JAILOC) return;
    const workspace = process.env.JAILOC_WORKSPACE || "unknown";
    api.slots.register({
      order: 150,
      slots: {
        sidebar_content() {
          const theme = api.theme.current;
          return (() => {
            var _el$ = _$createElement("box"),
              _el$2 = _$createElement("text"),
              _el$3 = _$createTextNode(`🔒 jailoc / `);
            _$insertNode(_el$, _el$2);
            _$setProp(_el$, "flexDirection", "column");
            _$insertNode(_el$2, _el$3);
            _$insert(_el$2, workspace, null);
            _$effect(_$p => _$setProp(_el$2, "color", theme.text.secondary, _$p));
            return _el$;
          })();
        }
      }
    });
    api.lifecycle.onDispose(watchOpenCode1(api, kind => writeNotification(kind)));
  }
};