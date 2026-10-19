import fs from "node:fs";
import path from "node:path";
import type { ContextReading } from "../../src/index.js";

const root = path.resolve(import.meta.dir, "../../../../internal/testsupport/contextfix/testdata");

export function expectedContext(name: "writer" | "reviewer" | "claude-parent" | "claude-child" | "unknown"): ContextReading {
  if (name === "claude-parent" || name === "claude-child") {
    const provenance = JSON.parse(fs.readFileSync(path.join(root, "claude/2.1.280/live-provenance.json"), "utf8"));
    const pair = provenance.pairs.find((p: {agent: string}) => p.agent === (name === "claude-parent" ? "parent" : "child"));
    return {used_tokens: pair.used_tokens, window_tokens: pair.window_tokens, fraction: pair.fraction, source: name === "claude-parent" ? "claude_jsonl_usage_statusline_capacity" : "claude_jsonl_usage_subagent_statusline_capacity", as_of: pair.usage_as_of};
  }
  const relative = name === "unknown" ? "unknown.expected.json" : `codex/0.159.2/${name}.expected.json`;
  return JSON.parse(fs.readFileSync(path.join(root, relative), "utf8"));
}
