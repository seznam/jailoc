// jailoc extension for OmO: shows the workspace in the footer status.
export default function (pi) {
  pi.on("session_start", async (_event, ctx) => {
    if (!process.env.JAILOC) return;
    const workspace = process.env.JAILOC_WORKSPACE || "unknown";
    ctx.ui.setStatus("jailoc", ctx.ui.theme.fg("dim", `🔒 jailoc / ${workspace}`));
  });
}
