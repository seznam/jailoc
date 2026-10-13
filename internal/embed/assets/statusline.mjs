// jailoc status line for Claude Code ("claude") and GitHub Copilot CLI
// ("copilot"): shows the workspace. For Claude Code, whose --settings
// replace the user's status line, it chains the status line command of
// the user's settings and prefixes its first line.
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";

const label = `🔒 jailoc / ${process.env.JAILOC_WORKSPACE || "unknown"}`;

function readJSON(file) {
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch {
    return undefined;
  }
}

// claudeStatusLine returns the status line command of the Claude Code
// settings with the highest precedence that set one.
function claudeStatusLine(session) {
  const project = session?.workspace?.project_dir || session?.cwd || process.cwd();
  const configDir = process.env.CLAUDE_CONFIG_DIR || join(homedir(), ".claude");
  const files = [
    join(project, ".claude", "settings.local.json"),
    join(project, ".claude", "settings.json"),
    join(configDir, "settings.json"),
  ];
  for (const file of files) {
    const statusLine = readJSON(file)?.statusLine;
    if (!statusLine) continue;
    if (statusLine.type !== "command" || typeof statusLine.command !== "string") return undefined;
    if (statusLine.command.includes("/etc/jailoc-tui-plugin/")) continue;
    return statusLine.command;
  }
  return undefined;
}

let input = "";
try {
  input = readFileSync(0, "utf8");
} catch {
  // No session data: show the label alone.
}

let lines = [];
if (process.argv[2] === "claude") {
  let session;
  try {
    session = JSON.parse(input);
  } catch {
    session = undefined;
  }
  const command = claudeStatusLine(session);
  if (command) {
    const out = spawnSync("/bin/sh", ["-c", command], { input, encoding: "utf8", timeout: 5000 });
    lines = (out.stdout || "").replace(/\n+$/, "").split("\n").filter((line, i) => i > 0 || line !== "");
  }
}

process.stdout.write([lines.length ? `${label}  ${lines[0]}` : label, ...lines.slice(1)].join("\n") + "\n");
