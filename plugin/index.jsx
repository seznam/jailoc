export default function(api) {
  if (!process.env.JAILOC) return;

  const workspace = process.env.JAILOC_WORKSPACE || "unknown";

  api.slots.register("sidebar_content", {
    order: 150,
    render: () => {
      const theme = api.theme.current;
      return (
        <box flexDirection="column">
          <text color={theme.text.secondary}>
            jailoc / {workspace}
          </text>
        </box>
      );
    }
  });
}
