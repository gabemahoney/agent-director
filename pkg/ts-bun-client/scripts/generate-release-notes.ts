/**
 * generate-release-notes.ts — Epic-grouped markdown release notes generator
 *
 * Reads a git log range and emits markdown with commits grouped by Epic ID
 * (the `b.xxx` bee shortcode extracted from conventional-commit subjects).
 *
 * HEREDOC SAFETY: git output is captured as a Buffer via Bun.spawn and decoded
 * as UTF-8 in TypeScript. ASCII unit-separator (0x1f) + record-separator (0x1e)
 * delimiters ensure that commit bodies containing backticks, $() subshells,
 * <<EOF sequences, or any other shell-special characters survive verbatim.
 * No shell string interpolation touches commit message content.
 *
 * Usage:
 *   bun run pkg/ts-bun-client/scripts/generate-release-notes.ts \
 *     --from <prev-tag> [--to HEAD] [--repo-root <path>] [--output <file>]
 *
 * Flags:
 *   --from <ref>       Git ref marking the start of the range (exclusive).
 *                      Typically the previous release tag (e.g. v0.7.4).
 *   --to <ref>         Git ref marking the end of the range (inclusive).
 *                      Defaults to HEAD.
 *   --repo-root <path> Repo root to run git in. Defaults to the repo root
 *                      resolved from this script's location.
 *   --output <path>    Write markdown to this file instead of stdout.
 *   --help             Print usage and exit 0.
 *
 * Output shape:
 *   # Release notes: <from>..<to>
 *
 *   ## b.xxx
 *
 *   - <commit subject> (`<sha7>`)
 *
 *   ## Other changes
 *
 *   - <commit subject> (`<sha7>`)
 */

import { existsSync } from "node:fs";
import { writeFile } from "node:fs/promises";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";

// ---------------------------------------------------------------------------
// Arg parsing
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);

if (args.includes("--help") || args.length === 0) {
  console.log(`generate-release-notes — Epic-grouped markdown release notes

Usage:
  bun run pkg/ts-bun-client/scripts/generate-release-notes.ts \\
    --from <prev-tag> [--to HEAD] [--repo-root <path>] [--output <file>]

Flags:
  --from <ref>       Start of range (exclusive). Usually the previous release tag.
  --to <ref>         End of range (inclusive). Defaults to HEAD.
  --repo-root <path> Repo root for git commands. Defaults to auto-detected root.
  --output <path>    Write output to file instead of stdout.
  --help             Print this message and exit.

HEREDOC SAFETY:
  Commit messages are captured via ASCII unit/record separators (0x1f/0x1e)
  and manipulated purely in TypeScript — backticks, \$(), <<EOF, and all other
  shell-special characters survive verbatim in the output.

Epic grouping:
  Commits with subjects matching /^\\w+\\((b\\.\\w+)\\)/ are grouped under their
  bee shortcode (e.g. feat(b.wvr): ... → ## b.wvr). All others go to
  ## Other changes.`);
  process.exit(0);
}

function getArg(flag: string): string | undefined {
  const idx = args.indexOf(flag);
  if (idx === -1) return undefined;
  const val = args[idx + 1];
  if (!val || val.startsWith("--")) {
    console.error(`Error: ${flag} requires a value`);
    process.exit(1);
  }
  return val;
}

const fromRef = getArg("--from");
const toRef = getArg("--to") ?? "HEAD";
const repoRootArg = getArg("--repo-root");
const outputPath = getArg("--output");

if (!fromRef) {
  console.error("Error: --from <ref> is required");
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Repo root resolution
// ---------------------------------------------------------------------------

function findRepoRoot(startDir: string): string {
  try {
    const result = execSync("git rev-parse --show-toplevel", {
      cwd: startDir,
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
    }).trim();
    if (result) return result;
  } catch {
    // fall through to walk-up
  }
  let dir = startDir;
  while (true) {
    if (existsSync(dir + "/.git")) return dir;
    const parent = dirname(dir);
    if (parent === dir) throw new Error("Could not locate repo root (no .git found)");
    dir = parent;
  }
}

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = repoRootArg ? resolve(repoRootArg) : findRepoRoot(scriptDir);

// ---------------------------------------------------------------------------
// Git log — ASCII-delimited, heredoc-safe
//
// Format string uses:
//   %H   = full commit SHA
//   %x1f = ASCII unit separator  (field delimiter within a record)
//   %s   = commit subject
//   %x1f = unit separator again
//   %b   = commit body
//   %x1e = ASCII record separator (record delimiter between commits)
//
// Splitting on 0x1e gives one entry per commit. Splitting each entry on 0x1f
// gives [sha, subject, body]. No shell eval of message content occurs.
// ---------------------------------------------------------------------------

const RS = "\x1e"; // record separator
const US = "\x1f"; // unit separator

const range = `${fromRef}..${toRef}`;

let rawOutput: string;
try {
  const proc = Bun.spawnSync(
    ["git", "log", range, "--format=%H%x1f%s%x1f%b%x1e"],
    { cwd: repoRoot, stderr: "inherit" }
  );
  if (proc.exitCode !== 0) {
    console.error(`Error: git log exited with code ${proc.exitCode}`);
    process.exit(1);
  }
  // Decode the raw bytes — preserves every byte verbatim
  rawOutput = new TextDecoder("utf-8").decode(proc.stdout);
} catch (err) {
  console.error(`Error running git log: ${err}`);
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Parse commits
// ---------------------------------------------------------------------------

interface Commit {
  sha: string;
  sha7: string;
  subject: string;
  body: string;
}

const commits: Commit[] = rawOutput
  .split(RS)
  .map((entry) => entry.trimEnd()) // strip trailing newlines after body
  .filter((entry) => entry.length > 0)
  .map((entry) => {
    const parts = entry.split(US);
    const sha = (parts[0] ?? "").trim();
    const subject = (parts[1] ?? "").trim();
    const body = (parts[2] ?? "").trim();
    return { sha, sha7: sha.slice(0, 7), subject, body };
  })
  .filter((c) => c.sha.length > 0);

// ---------------------------------------------------------------------------
// Group by Epic ID
//
// Convention: feat(b.wvr): message → Epic ID = "b.wvr"
// Regex: /^\w+\((b\.\w+)\)/  — matches "word(b.word)" at start of subject.
// Insertion-order grouping (Map preserves order in modern JS).
// ---------------------------------------------------------------------------

const EPIC_RE = /^\w+\((b\.\w+)\)/;

const epicGroups = new Map<string, Commit[]>();
const ungrouped: Commit[] = [];

for (const commit of commits) {
  const m = EPIC_RE.exec(commit.subject);
  if (m) {
    const epicId = m[1]!;
    if (!epicGroups.has(epicId)) epicGroups.set(epicId, []);
    epicGroups.get(epicId)!.push(commit);
  } else {
    ungrouped.push(commit);
  }
}

// ---------------------------------------------------------------------------
// Render markdown
// ---------------------------------------------------------------------------

const lines: string[] = [];

lines.push(`# Release notes: ${range}`);
lines.push("");
lines.push(`Generated ${new Date().toISOString().slice(0, 10)}.`);
lines.push("");

if (epicGroups.size === 0 && ungrouped.length === 0) {
  lines.push("_No commits found in this range._");
} else {
  for (const [epicId, epCommits] of epicGroups) {
    lines.push(`## ${epicId}`);
    lines.push("");
    for (const c of epCommits) {
      lines.push(`- ${c.subject} (\`${c.sha7}\`)`);
    }
    lines.push("");
  }

  if (ungrouped.length > 0) {
    lines.push("## Other changes");
    lines.push("");
    for (const c of ungrouped) {
      lines.push(`- ${c.subject} (\`${c.sha7}\`)`);
    }
    lines.push("");
  }
}

const markdown = lines.join("\n");

// ---------------------------------------------------------------------------
// Output
// ---------------------------------------------------------------------------

if (outputPath) {
  await writeFile(outputPath, markdown, "utf-8");
  console.error(`[notes] wrote ${markdown.length} bytes to ${outputPath}`);
} else {
  process.stdout.write(markdown + "\n");
}
