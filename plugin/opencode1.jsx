/** @jsxImportSource @opentui/solid */

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
  },
};
