/** @jsxImportSource @opentui/solid */
import { Plugin } from "@opencode/plugin/tui";

export default Plugin.define({
  id: "jailoc",
  setup(context) {
    if (!process.env.JAILOC) return;

    const workspace = process.env.JAILOC_WORKSPACE || "unknown";

    return context.ui.slot({
      append: "sidebar.content",
      render: () => (
        <box flexDirection="column">
          <text fg={context.theme.text.muted}>🔒 jailoc / {workspace}</text>
        </box>
      ),
    });
  },
});
