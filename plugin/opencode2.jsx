/** @jsxImportSource @opentui/solid */
import { Plugin } from "@opencode/plugin/tui";
import { writeNotification } from "./notify-core.mjs";
import { openCode2BuiltInNotifies, watchOpenCode2 } from "./notify-opencode2.mjs";

export default Plugin.define({
  id: "jailoc",
  setup(context) {
    if (!process.env.JAILOC) return;

    const workspace = process.env.JAILOC_WORKSPACE || "unknown";

    const removeSlot = context.ui.slot({
      append: "sidebar.content",
      render: () => (
        <box flexDirection="column">
          <text fg={context.theme.text.muted}>🔒 jailoc / {workspace}</text>
        </box>
      ),
    });
    const stopNotifications = watchOpenCode2(context, {
      emit: (kind) => writeNotification(kind),
      builtInNotifies: () => openCode2BuiltInNotifies(),
    });

    return () => {
      stopNotifications();
      removeSlot();
    };
  },
});
