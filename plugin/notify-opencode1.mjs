// jailoc notification producer for the OpenCode 1.19.5 TUI plugin API. The
// build inlines this module into the OpenCode 1 plugin.
import { boundedSet } from "./notify-core.mjs";

// The built-in plugin that raises OpenCode 1's own attention notifications.
const openCode1NotificationsPlugin = "internal:notifications";

// openCode1BuiltInNotifies reports whether OpenCode 1 raises its own system
// notifications: attention and its notifications enabled in the resolved TUI
// config, and the built-in notifications plugin not deactivated.
function openCode1BuiltInNotifies(api) {
  const attention = api.tuiConfig.attention;
  if (!attention.enabled || !attention.notifications) return false;
  const status = api.plugins.list().find((plugin) => plugin.id === openCode1NotificationsPlugin);
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
  return (
    info.role === "assistant" &&
    info.error == null &&
    typeof info.finish === "string" &&
    !openCode1StepFinishes.has(info.finish)
  );
}

// watchOpenCode1 calls emit("completed") when a root session's turn ends
// (busy or retry, then idle, without session.error) and the turn's last
// assistant message update is a finished reply, and emit("input") when a
// root session asks a permission or a question. It returns a function that
// unsubscribes the handlers and drops the state.
export function watchOpenCode1(api, emit) {
  const active = boundedSet();
  const errored = boundedSet();
  const replied = boundedSet();
  const requests = boundedSet();

  const notify = (kind, sessionID) => {
    if (!openCode1IsRoot(api, sessionID)) return;
    if (openCode1BuiltInNotifies(api)) return;
    emit(kind);
  };
  const asked = (event) => {
    if (requests.has(event.properties.id)) return;
    requests.add(event.properties.id);
    notify("input", event.properties.sessionID);
  };
  const settled = (event) => requests.delete(event.properties.requestID);

  const unsubscribe = [
    api.event.on("session.status", (event) => {
      const { sessionID, status } = event.properties;
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
    }),
    api.event.on("message.updated", (event) => {
      const info = event.properties.info;
      if (info.role !== "assistant" || !active.has(info.sessionID)) return;
      if (openCode1Replied(info)) replied.add(info.sessionID);
      else replied.delete(info.sessionID);
    }),
    api.event.on("session.error", (event) => {
      const sessionID = event.properties.sessionID;
      if (sessionID !== undefined && active.has(sessionID)) errored.add(sessionID);
    }),
    api.event.on("permission.asked", asked),
    api.event.on("question.asked", asked),
    api.event.on("permission.replied", settled),
    api.event.on("question.replied", settled),
    api.event.on("question.rejected", settled),
  ];

  return () => {
    for (const stop of unsubscribe) stop();
    active.clear();
    errored.clear();
    replied.clear();
    requests.clear();
  };
}
