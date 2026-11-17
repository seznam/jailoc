import { test } from "node:test";
import assert from "node:assert/strict";
import { boundedSet, writeNotification } from "./notify-core.mjs";

test("writeNotification writes the exact jailoc-notify sequence for each kind", () => {
  const written = [];
  const write = (chunk) => written.push(chunk);

  writeNotification("completed", write);
  writeNotification("input", write);

  assert.deepEqual(written, ["\x1b]777;jailoc-notify;completed\x07", "\x1b]777;jailoc-notify;input\x07"]);
});

test("writeNotification rejects kinds outside the jailoc-notify protocol", () => {
  assert.throws(() => writeNotification("done", () => {}), /unknown jailoc notification kind/);
});

test("boundedSet evicts the oldest entry when it exceeds its limit", () => {
  const set = boundedSet(2);

  set.add("a");
  set.add("b");
  set.add("c");

  assert.deepEqual([...set], ["b", "c"]);
});
