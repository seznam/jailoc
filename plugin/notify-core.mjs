// Shared, non-visual helpers for the jailoc notification producers. The build
// inlines this module into each TUI plugin, so it must stay self-contained.

// The private jailoc OSC 777 subcommand. The jailoc host consumes these
// sequences from the session output; they are not desktop notifications.
const notificationSequences = {
  completed: "\x1b]777;jailoc-notify;completed\x07",
  input: "\x1b]777;jailoc-notify;input\x07",
};

// Upper bound for every per-session or per-request set a producer keeps.
const notifyStateLimit = 256;

// writeNotification sends one jailoc notification of kind "completed" or
// "input". The TUI renderers leave process.stdout in passthrough mode, so the
// sequence reaches the session output unchanged.
export function writeNotification(kind, write = (chunk) => process.stdout.write(chunk)) {
  const sequence = Object.hasOwn(notificationSequences, kind) ? notificationSequences[kind] : undefined;
  if (sequence === undefined) throw new Error(`unknown jailoc notification kind: ${kind}`);
  write(sequence);
}

// boundedSet returns a set that forgets its oldest entries beyond limit, so
// state for sessions or requests that never settle cannot grow without end.
export function boundedSet(limit = notifyStateLimit) {
  const values = new Set();
  return {
    add(value) {
      values.delete(value);
      values.add(value);
      if (values.size > limit) values.delete(values.values().next().value);
    },
    has: (value) => values.has(value),
    delete: (value) => values.delete(value),
    clear: () => values.clear(),
    [Symbol.iterator]: () => values.values(),
  };
}
