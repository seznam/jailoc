import { test } from "node:test";
import assert from "node:assert/strict";
import { watchOpenCode1 } from "./notify-opencode1.mjs";

// fakeApi models the OpenCode 1.19.5 TUI plugin API surface the producer uses:
// api.event.on(type, handler) passes the whole { type, properties } event and
// returns an unsubscribe function; api.state.session.get returns the synced
// session or undefined.
function fakeApi({ sessions = {}, attention = { enabled: false, notifications: true }, plugins = [] } = {}) {
  const handlers = new Map();
  const api = {
    tuiConfig: { attention },
    plugins: { list: () => plugins },
    state: { session: { get: (id) => sessions[id] } },
    event: {
      on(type, handler) {
        if (!handlers.has(type)) handlers.set(type, new Set());
        handlers.get(type).add(handler);
        return () => handlers.get(type).delete(handler);
      },
    },
  };
  const fire = (type, properties) => {
    for (const handler of handlers.get(type) ?? []) handler({ id: "evt", type, properties });
  };
  const subscriptions = () => [...handlers.values()].reduce((count, set) => count + set.size, 0);
  return { api, fire, subscriptions };
}

const status = (sessionID, type) => ["session.status", { sessionID, status: { type } }];
// message is a message.updated event as OpenCode 1.19.5 publishes it from
// Session.updateMessage: { sessionID, info } with the whole message.
const message = (sessionID, info) => ["message.updated", { sessionID, info: { sessionID, ...info } }];
const assistant = (sessionID, id, fields = {}) =>
  message(sessionID, { id, role: "assistant", parentID: "msg_user", time: { created: 1 }, ...fields });
// reply is the assistant message of a model step that finished normally.
const reply = (sessionID, id = "msg_reply") => assistant(sessionID, id, { finish: "stop", time: { created: 1, completed: 2 } });
// aborted is finalizeInterruptedAssistant's update of an interrupted message.
const aborted = (sessionID, id = "msg_reply", fields = {}) =>
  assistant(sessionID, id, {
    error: { name: "MessageAbortedError", data: { message: "Aborted" } },
    time: { created: 1, completed: 2 },
    ...fields,
  });
const sessions = { root: { id: "root" }, child: { id: "child", parentID: "root" } };

function watch(options) {
  const fake = fakeApi(options);
  const emitted = [];
  const dispose = watchOpenCode1(fake.api, (kind) => emitted.push(kind));
  return { ...fake, emitted, dispose };
}

test("root session busy then idle emits completed once", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...status("root", "idle"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, ["completed"]);
});

test("root session retry then idle emits completed", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "retry"));
  fire(...reply("root"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, ["completed"]);
});

test("idle without a preceding busy status emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("turn ending in session.error emits nothing and the next turn emits again", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire("session.error", { sessionID: "root", error: { name: "MessageAbortedError", data: {} } });
  fire(...status("root", "idle"));
  assert.deepEqual(emitted, []);

  fire(...status("root", "busy"));
  fire(...reply("root", "msg_next"));
  fire(...status("root", "idle"));
  assert.deepEqual(emitted, ["completed"]);
});

test("subagent session turn emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("child", "busy"));
  fire(...reply("child"));
  fire(...status("child", "idle"));

  assert.deepEqual(emitted, []);
});

test("session missing from synced state emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("ghost", "busy"));
  fire(...status("ghost", "idle"));
  fire("permission.asked", { id: "per_1", sessionID: "ghost" });
  fire("question.asked", { id: "que_1", sessionID: "ghost" });

  assert.deepEqual(emitted, []);
});

test("a status that turns busy again before idle keeps the turn's reply", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...status("root", "busy"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, ["completed"]);
});

test("busy then idle without a finished reply emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("turn interrupted before the stream starts emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...assistant("root", "msg_reply"));
  fire(...aborted("root"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("cancelled shell command emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...message("root", { id: "msg_user", role: "user", time: { created: 1 } }));
  fire(...assistant("root", "msg_shell"));
  fire(...assistant("root", "msg_shell", { time: { created: 1, completed: 2 } }));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("a finished reply that is then aborted emits nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...aborted("root", "msg_reply", { finish: "stop" }));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("a reply that stopped for tool calls is not a finished turn", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...assistant("root", "msg_step", { finish: "tool-calls", time: { created: 1, completed: 2 } }));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("an aborted later step outweighs an earlier finished reply", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("root", "msg_1"));
  fire(...assistant("root", "msg_2"));
  fire(...aborted("root", "msg_2"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("a reply from the previous turn does not complete the next one", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...status("root", "idle"));
  fire(...status("root", "busy"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, ["completed"]);
});

test("a subagent reply does not complete the root turn", () => {
  const { fire, emitted } = watch({ sessions });

  fire(...status("root", "busy"));
  fire(...reply("child"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, []);
});

test("root permission and question requests emit input once per request", () => {
  const { fire, emitted } = watch({ sessions });

  fire("permission.asked", { id: "per_1", sessionID: "root" });
  fire("permission.asked", { id: "per_1", sessionID: "root" });
  fire("question.asked", { id: "que_1", sessionID: "root" });

  assert.deepEqual(emitted, ["input", "input"]);
});

test("replied request ids are forgotten", () => {
  const { fire, emitted } = watch({ sessions });

  fire("permission.asked", { id: "per_1", sessionID: "root" });
  fire("permission.replied", { sessionID: "root", requestID: "per_1", reply: "once" });
  fire("permission.asked", { id: "per_1", sessionID: "root" });
  fire("question.asked", { id: "que_1", sessionID: "root" });
  fire("question.rejected", { sessionID: "root", requestID: "que_1" });
  fire("question.asked", { id: "que_1", sessionID: "root" });

  assert.deepEqual(emitted, ["input", "input", "input", "input"]);
});

test("subagent permission and question requests emit nothing", () => {
  const { fire, emitted } = watch({ sessions });

  fire("permission.asked", { id: "per_1", sessionID: "child" });
  fire("question.asked", { id: "que_1", sessionID: "child" });

  assert.deepEqual(emitted, []);
});

test("enabled built-in attention notifications take over", () => {
  const { fire, emitted } = watch({ sessions, attention: { enabled: true, notifications: true } });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...status("root", "idle"));
  fire("permission.asked", { id: "per_1", sessionID: "root" });

  assert.deepEqual(emitted, []);
});

test("built-in attention without notifications leaves notifying to jailoc", () => {
  const { fire, emitted } = watch({ sessions, attention: { enabled: true, notifications: false } });

  fire(...status("root", "busy"));
  fire(...reply("root"));
  fire(...status("root", "idle"));

  assert.deepEqual(emitted, ["completed"]);
});

test("deactivated built-in notifications plugin leaves notifying to jailoc", () => {
  const { fire, emitted } = watch({
    sessions,
    attention: { enabled: true, notifications: true },
    plugins: [{ id: "internal:notifications", active: false }],
  });

  fire("question.asked", { id: "que_1", sessionID: "root" });

  assert.deepEqual(emitted, ["input"]);
});

test("dispose unsubscribes every handler", () => {
  const { fire, emitted, dispose, subscriptions } = watch({ sessions });
  fire(...status("root", "busy"));

  dispose();
  fire(...status("root", "idle"));

  assert.equal(subscriptions(), 0);
  assert.deepEqual(emitted, []);
});
