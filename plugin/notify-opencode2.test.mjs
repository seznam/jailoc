import { test } from "node:test";
import assert from "node:assert/strict";
import { watchOpenCode2 } from "./notify-opencode2.mjs";

// fakeContext models the OpenCode 2.2.5 TUI plugin context surface the
// producer uses: context.data.on(type, handler) passes the whole
// { type, data } event and returns an unsubscribe function; the session store
// is already updated when handlers run.
function fakeContext(sessions) {
  const handlers = new Map();
  const running = new Set();
  const ancestors = (id) => {
    const chain = [];
    for (let parent = sessions[id]?.parentID; parent; parent = sessions[parent]?.parentID) chain.push(parent);
    return chain;
  };
  const context = {
    data: {
      on(type, handler) {
        if (!handlers.has(type)) handlers.set(type, new Set());
        handlers.get(type).add(handler);
        return () => handlers.get(type).delete(handler);
      },
      session: {
        get: (id) => sessions[id],
        family: (id) => Object.keys(sessions).filter((other) => other === id || ancestors(other).includes(id)),
        status: (id) => (running.has(id) ? "running" : "idle"),
      },
    },
  };
  const fire = (type, data) => {
    if (type === "session.execution.started") running.add(data.sessionID);
    if (/^session\.execution\.(succeeded|failed|interrupted)$/.test(type)) running.delete(data.sessionID);
    for (const handler of handlers.get(type) ?? []) handler({ id: "evt", type, data });
  };
  const subscriptions = () => [...handlers.values()].reduce((count, set) => count + set.size, 0);
  return { context, fire, subscriptions };
}

// fakeTimers runs due callbacks on run(). With clearing off, clear() keeps
// the callback pending, modelling a callback that already got queued.
function fakeTimers({ clearing = true } = {}) {
  const pending = new Map();
  let next = 0;
  return {
    set(fn) {
      next += 1;
      pending.set(next, fn);
      return next;
    },
    clear(handle) {
      if (clearing) pending.delete(handle);
    },
    size: () => pending.size,
    run() {
      const due = [...pending.values()];
      pending.clear();
      for (const fn of due) fn();
    },
  };
}

const sessions = {
  root: { id: "root" },
  child: { id: "child", parentID: "root" },
  other: { id: "other", parentID: "root" },
};

function watch({ builtIn = false, clearTimers = true } = {}) {
  const fake = fakeContext(sessions);
  const timers = fakeTimers({ clearing: clearTimers });
  const emitted = [];
  const dispose = watchOpenCode2(fake.context, {
    emit: (kind) => emitted.push(kind),
    builtInNotifies: () => builtIn,
    timers,
  });
  return { ...fake, timers, emitted, dispose };
}

const run = (fire, sessionID, end = "succeeded", extra = {}) => {
  fire("session.execution.started", { sessionID });
  fire(`session.execution.${end}`, { sessionID, ...extra });
};

test("root execution success emits completed", () => {
  const { fire, emitted } = watch();

  run(fire, "root");

  assert.deepEqual(emitted, ["completed"]);
});

test("subagent execution success emits nothing", () => {
  const { fire, emitted } = watch();

  run(fire, "child");

  assert.deepEqual(emitted, []);
});

test("session missing from the store emits nothing", () => {
  const { fire, emitted } = watch();

  run(fire, "ghost");
  fire("permission.asked", { id: "per_1", sessionID: "ghost", action: "bash", resources: [] });
  fire("form.created", { form: { id: "frm_1", sessionID: "ghost", title: "Pick" } });

  assert.deepEqual(emitted, []);
});

test("failed and interrupted root executions emit nothing", () => {
  const { fire, emitted } = watch();

  run(fire, "root", "failed", { error: { type: "unknown", message: "boom" } });
  run(fire, "root", "interrupted", { reason: "user" });

  assert.deepEqual(emitted, []);
});

test("root success while a subagent runs waits for the root to finish after the subagent", () => {
  const { fire, emitted, timers } = watch();
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");
  assert.deepEqual(emitted, []);

  fire("session.execution.succeeded", { sessionID: "child" });
  fire("session.execution.started", { sessionID: "root" });
  timers.run();
  assert.deepEqual(emitted, []);

  fire("session.execution.succeeded", { sessionID: "root" });
  timers.run();

  assert.deepEqual(emitted, ["completed"]);
});

test("pending root completion is emitted when no wake follows the last subagent", () => {
  const { fire, emitted, timers } = watch();
  fire("session.execution.started", { sessionID: "child" });
  fire("session.execution.started", { sessionID: "other" });
  run(fire, "root");

  fire("session.execution.interrupted", { sessionID: "child", reason: "user" });
  timers.run();
  assert.deepEqual(emitted, []);

  fire("session.execution.failed", { sessionID: "other", error: { type: "unknown", message: "boom" } });
  timers.run();

  assert.deepEqual(emitted, ["completed"]);
});

test("pending root completion is dropped when the root is interrupted", () => {
  const { fire, emitted, timers } = watch();
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");

  fire("session.execution.interrupted", { sessionID: "root", reason: "user" });
  fire("session.execution.succeeded", { sessionID: "child" });
  timers.run();

  assert.deepEqual(emitted, []);
});

test("a direct subagent starting while the root completion settles holds it until that subagent ends", () => {
  const { fire, emitted, timers } = watch();
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");
  fire("session.execution.succeeded", { sessionID: "child" });
  assert.equal(timers.size(), 1);

  fire("session.execution.started", { sessionID: "other" });
  assert.equal(timers.size(), 0);
  timers.run();
  assert.deepEqual(emitted, []);

  fire("session.execution.succeeded", { sessionID: "other" });
  timers.run();

  assert.deepEqual(emitted, ["completed"]);
});

test("a settle callback that fires after a direct subagent started emits nothing and keeps waiting", () => {
  const { fire, emitted, timers } = watch({ clearTimers: false });
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");
  fire("session.execution.succeeded", { sessionID: "child" });

  fire("session.execution.started", { sessionID: "other" });
  timers.run();
  assert.deepEqual(emitted, []);

  fire("session.execution.succeeded", { sessionID: "other" });
  timers.run();

  assert.deepEqual(emitted, ["completed"]);
});

test("a settle callback that fires after the root started again emits nothing", () => {
  const { fire, emitted, timers } = watch({ clearTimers: false });
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");
  fire("session.execution.succeeded", { sessionID: "child" });

  fire("session.execution.started", { sessionID: "root" });
  timers.run();

  assert.deepEqual(emitted, []);
});

test("root permission and form requests emit input once per request", () => {
  const { fire, emitted } = watch();

  fire("permission.asked", { id: "per_1", sessionID: "root", action: "bash", resources: [] });
  fire("permission.asked", { id: "per_1", sessionID: "root", action: "bash", resources: [] });
  fire("form.created", { form: { id: "frm_1", sessionID: "root", title: "Pick" } });
  fire("form.cancelled", { id: "frm_1", sessionID: "root" });
  fire("form.created", { form: { id: "frm_1", sessionID: "root", title: "Pick" } });

  assert.deepEqual(emitted, ["input", "input", "input"]);
});

test("subagent permission and form requests emit nothing", () => {
  const { fire, emitted } = watch();

  fire("permission.asked", { id: "per_1", sessionID: "child", action: "bash", resources: [] });
  fire("form.created", { form: { id: "frm_1", sessionID: "child", title: "Pick" } });

  assert.deepEqual(emitted, []);
});

test("enabled built-in notifications take over", () => {
  const { fire, emitted } = watch({ builtIn: true });

  run(fire, "root");
  fire("permission.asked", { id: "per_1", sessionID: "root", action: "bash", resources: [] });

  assert.deepEqual(emitted, []);
});

test("dispose unsubscribes every handler and cancels pending timers", () => {
  const { fire, emitted, timers, dispose, subscriptions } = watch();
  fire("session.execution.started", { sessionID: "child" });
  run(fire, "root");
  fire("session.execution.succeeded", { sessionID: "child" });
  assert.equal(timers.size(), 1);

  dispose();
  timers.run();

  assert.equal(subscriptions(), 0);
  assert.equal(timers.size(), 0);
  assert.deepEqual(emitted, []);
});
