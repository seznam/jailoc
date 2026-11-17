/** @jsxImportSource @opentui/solid */
import { writeNotification } from "./notify-core.mjs";
import { watchOpenCode1 } from "./notify-opencode1.mjs";

export default {
  id: "jailoc",
  tui: (api) => {
    if (!process.env.JAILOC) return;

    const workspace = process.env.JAILOC_WORKSPACE || "unknown";

    api.slots.register({
      order: 150,
      slots: {
        sidebar_content() {
          const theme = api.theme.current;

          return (
            <box flexDirection="column">
              <text color={theme.text.secondary}>🔒 jailoc / {workspace}</text>
            </box>
          );
        },
      },
    });

    api.lifecycle.onDispose(watchOpenCode1(api, (kind) => writeNotification(kind)));
  },
};
