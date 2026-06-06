/**
 * check-source-of-truth.ts — SR-16 source-of-truth invariant gate
 *
 * Enforces that `pkg/ts-bun-client/package.json` is the ONE authoritative
 * version site in this repo. Exits 0 (silently) when clean. Emits one JSON
 * line per violation to stderr and exits 1 when any violation is found.
 *
 * Usage (from repo root):
 *   bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts
 *   bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts --help
 *   bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts --quiet
 *
 * Flags:
 *   --help    Print patterns + exit semantics, then exit 0.
 *   --quiet   (no-op — the script is already silent on clean; kept for
 *             scripting convenience so callers can pass it unconditionally)
 *
 * ── Patterns detected ────────────────────────────────────────────────────────
 *
 * P1  OTHER package.json WITH top-level "version"
 *     Any package.json found recursively in the repo (outside node_modules/,
 *     dist/, .git/, testdata/, test/fixtures/) other than the authoritative
 *     pkg/ts-bun-client/package.json that carries a top-level `"version"`
 *     field is a violation.
 *
 * P2  SKILL.md frontmatter `version:` line
 *     Any SKILL.md file found recursively under skills/ whose YAML
 *     frontmatter block contains a `version:` key is a violation.
 *
 * P3  Makefile literal VERSION assignment
 *     Any line in Makefile matching `^[A-Z_]*VERSION[A-Z_]*\s*[:?]?=\s*\d+\.\d+\.\d+`
 *     is a violation — unless it is a derived expression (contains `$(shell jq`,
 *     `RELEASE_VERSION`, or a reference to `pkg/ts-bun-client/package.json`).
 *
 * P4  Go literal version constant in internal/version/
 *     Any Go file under internal/version/ that contains
 *       var Version = "X.Y.Z"  or  const Version = "X.Y.Z"
 *     where X.Y.Z is a real SemVer (not "dev", not "0.0.0-dev", not stamped
 *     via -ldflags) is a violation.
 *
 * P5  dist/ JSON files with a top-level "version" field
 *     Any *.json file under pkg/ts-bun-client/dist/ that is parseable JSON
 *     and contains a top-level `"version"` field is a violation (dist
 *     artifacts must be derived from package.json at build time, not embed
 *     an independent authoritative version).  If dist/ does not exist the
 *     check is silently skipped.
 *
 * ── Non-authoritative mentions that NEVER trigger ────────────────────────────
 *   - Prose / comments / README / CHANGELOG / docs/**
 *   - Test files (*.test.ts, *_test.go, test/ subtrees, testdata/, fixtures/)
 *   - version-floor.json (carries min_binary_version, not a release version)
 *   - Makefile lines that are derived from package.json via jq / RELEASE_VERSION
 *   - Go files with the sentinel "dev" or "0.0.0-dev"
 *
 * ── Output format ────────────────────────────────────────────────────────────
 *   On clean:     silent, exit 0.
 *   On violation: one JSON object per violation on stderr, then exit 1:
 *     {
 *       "gate": "invariant.source-of-truth",
 *       "offending_file": "relative/path/to/file",
 *       "offending_field": "<field or line description>",
 *       "description": "human-readable explanation"
 *     }
 */

import { existsSync, readFileSync } from "node:fs";
import { resolve, dirname, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as path from "node:path";

// ---------------------------------------------------------------------------
// Flag parsing
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);

if (args.includes("--help")) {
  console.log(`check-source-of-truth — SR-16 invariant gate

Usage:
  bun run pkg/ts-bun-client/scripts/check-source-of-truth.ts [--help] [--quiet]

Exits 0 (silently) when clean.
Exits 1 with one JSON-per-line violation report on stderr when any
authoritative version site beyond pkg/ts-bun-client/package.json is found.

Patterns detected:
  P1  Other package.json files with a top-level "version" field
  P2  SKILL.md frontmatter containing a "version:" key
  P3  Makefile literal VERSION assignments (not derived from package.json)
  P4  Go literal version constant in internal/version/ (not sentinel "dev")
  P5  dist/ JSON files containing a top-level "version" field

See the script header for full exclusion rules.`);
  process.exit(0);
}

// --quiet is accepted silently (the script is already silent on clean)

// ---------------------------------------------------------------------------
// Locate repo root
// ---------------------------------------------------------------------------

function findRepoRoot(startDir: string): string {
  // Try git first (fast)
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
  // Walk up looking for .git
  let dir = startDir;
  while (true) {
    if (existsSync(path.join(dir, ".git"))) return dir;
    const parent = path.dirname(dir);
    if (parent === dir) throw new Error("Could not locate repo root (no .git found)");
    dir = parent;
  }
}

const scriptDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = findRepoRoot(scriptDir);

// ---------------------------------------------------------------------------
// Violation accumulator
// ---------------------------------------------------------------------------

interface Violation {
  gate: "invariant.source-of-truth";
  offending_file: string;
  offending_field: string;
  description: string;
}

const violations: Violation[] = [];

function addViolation(filePath: string, field: string, description: string): void {
  violations.push({
    gate: "invariant.source-of-truth",
    offending_file: relative(repoRoot, filePath),
    offending_field: field,
    description,
  });
}

// ---------------------------------------------------------------------------
// Filesystem helpers
// ---------------------------------------------------------------------------

/** Recursively list files matching a predicate, skipping pruned directories. */
function walkFiles(
  dir: string,
  predicate: (filePath: string) => boolean,
  pruneDir: (dirPath: string) => boolean,
): string[] {
  const results: string[] = [];
  if (!existsSync(dir)) return results;
  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      if (!pruneDir(fullPath)) {
        results.push(...walkFiles(fullPath, predicate, pruneDir));
      }
    } else if (entry.isFile() && predicate(fullPath)) {
      results.push(fullPath);
    }
  }
  return results;
}

// Standard prune set (shared across P1 and P5)
function isStandardPruneDir(dirPath: string): boolean {
  const rel = relative(repoRoot, dirPath);
  const parts = rel.split(path.sep);
  return parts.some((p) =>
    p === "node_modules" ||
    p === ".git" ||
    p === "dist" ||
    p === "testdata" ||
    p === "fixtures"
  );
}

// ---------------------------------------------------------------------------
// P1 — Other package.json files with a top-level "version" field
// ---------------------------------------------------------------------------

const AUTHORITATIVE_PKG = resolve(repoRoot, "pkg/ts-bun-client/package.json");

function checkP1(): void {
  const pkgFiles = walkFiles(
    repoRoot,
    (f) => path.basename(f) === "package.json",
    isStandardPruneDir,
  );

  for (const pkgPath of pkgFiles) {
    // Skip the authoritative source
    if (pkgPath === AUTHORITATIVE_PKG) continue;

    // Skip test fixture trees
    const rel = relative(repoRoot, pkgPath);
    if (rel.includes("test/fixtures") || rel.includes("testdata")) continue;

    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(readFileSync(pkgPath, "utf8")) as Record<string, unknown>;
    } catch {
      continue; // unparseable JSON is not an authoritative site
    }

    if (typeof parsed["version"] === "string" && parsed["version"].length > 0) {
      addViolation(
        pkgPath,
        "version",
        `package.json at ${rel} has a top-level "version" field ("${parsed["version"]}"). ` +
          `Only pkg/ts-bun-client/package.json is authoritative (SR-16).`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// P2 — SKILL.md frontmatter `version:` line
// ---------------------------------------------------------------------------

const FRONTMATTER_VERSION_RE = /^version\s*:/m;

function checkP2(): void {
  const skillFiles = walkFiles(
    repoRoot,
    (f) => path.basename(f) === "SKILL.md",
    (d) => {
      const rel = relative(repoRoot, d);
      return rel.startsWith(".git");
    },
  );

  for (const skillPath of skillFiles) {
    const content = readFileSync(skillPath, "utf8");

    // Extract YAML frontmatter block (between first and second `---` lines)
    const fmMatch = content.match(/^---\r?\n([\s\S]*?)\r?\n---/);
    if (!fmMatch) continue;

    const frontmatter = fmMatch[1];
    if (FRONTMATTER_VERSION_RE.test(frontmatter)) {
      const line = frontmatter
        .split("\n")
        .find((l) => /^version\s*:/.test(l)) ?? "version:";
      addViolation(
        skillPath,
        line.trim(),
        `SKILL.md at ${relative(repoRoot, skillPath)} has a "version:" key in YAML frontmatter. ` +
          `Version fields must be removed from SKILL.md files (SR-16).`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// P3 — Makefile literal VERSION assignment (not derived)
// ---------------------------------------------------------------------------

// Matches: [A-Z_]*VERSION[A-Z_]* [whitespace] [:?]?= [whitespace] X.Y.Z
// The version must be a pure X.Y.Z with no pre-release suffix (e.g. "0.0.0-dev"
// is the dev sentinel and is NOT considered an authoritative release site).
const MAKEFILE_VERSION_RE = /^[A-Z_]*VERSION[A-Z_]*\s*[:?]?=\s*(\d+\.\d+\.\d+)(?!\S)/;

// Exclusions: lines that derive from package.json, not literals.
function isMakefileDerivedLine(line: string): boolean {
  return (
    line.includes("$(shell jq") ||
    line.includes("RELEASE_VERSION") ||
    line.includes("pkg/ts-bun-client/package.json") ||
    line.includes("$(AGENT_DIRECTOR_BUILD_VERSION)") ||
    line.includes("$(if ")
  );
}

// Exclusions: variable names that have a multi-component prefix before VERSION,
// indicating they track a third-party dependency pin rather than the project's
// own release version.  E.g. CLAUDE_CODE_VERSION (prefix = "CLAUDE_CODE", two
// underscore-separated components) is a tool pin, not a release version site.
// Single-component prefixes like RELEASE_, APP_, PKG_ are still flagged.
function isMakefileToolPin(varName: string): boolean {
  // Extract the portion of the name that precedes "VERSION"
  const versionIdx = varName.indexOf("VERSION");
  if (versionIdx <= 0) return false; // no prefix, or VERSION is not present
  const prefix = varName.slice(0, versionIdx).replace(/_$/, ""); // strip trailing _
  const components = prefix.split("_").filter((c) => c.length > 0);
  return components.length >= 2;
}

function checkP3(): void {
  const makefilePath = resolve(repoRoot, "Makefile");
  if (!existsSync(makefilePath)) return;

  const lines = readFileSync(makefilePath, "utf8").split("\n");
  lines.forEach((line, idx) => {
    const m = MAKEFILE_VERSION_RE.exec(line);
    if (!m) return;
    if (isMakefileDerivedLine(line)) return;

    // Extract the variable name to check for tool-pin exclusion
    const varNameMatch = /^([A-Z_]*VERSION[A-Z_]*)/.exec(line);
    const varName = varNameMatch ? varNameMatch[1] : "";
    if (isMakefileToolPin(varName)) return;

    addViolation(
      makefilePath,
      line.trim(),
      `Makefile line ${idx + 1} contains a literal VERSION assignment: "${line.trim()}". ` +
        `VERSION values must be derived from pkg/ts-bun-client/package.json (SR-16).`,
    );
  });
}

// ---------------------------------------------------------------------------
// P4 — Go literal version constant in internal/version/
// ---------------------------------------------------------------------------

// Matches: var Version = "X.Y.Z"  or  const Version = "X.Y.Z"
// where X.Y.Z is a real semver — NOT "dev", NOT "0.0.0-dev"
const GO_LITERAL_VERSION_RE =
  /(?:var|const)\s+Version\s*=\s*"(\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?)"/;

const GO_SENTINEL_RE = /^(?:dev|0\.0\.0-dev)$/;

function checkP4(): void {
  const versionDir = resolve(repoRoot, "internal/version");
  if (!existsSync(versionDir)) return;

  const goFiles = walkFiles(
    versionDir,
    (f) => f.endsWith(".go") && !f.endsWith("_test.go"),
    () => false,
  );

  for (const goPath of goFiles) {
    const content = readFileSync(goPath, "utf8");
    const match = GO_LITERAL_VERSION_RE.exec(content);
    if (!match) continue;

    const versionStr = match[1];
    // Sentinels are OK
    if (GO_SENTINEL_RE.test(versionStr)) continue;

    addViolation(
      goPath,
      match[0],
      `Go file ${relative(repoRoot, goPath)} contains a literal version constant: ${match[0]}. ` +
        `Go version must be stamped via -ldflags at build time, not hardcoded (SR-16).`,
    );
  }
}

// ---------------------------------------------------------------------------
// P5 — dist/ JSON files with a top-level "version" field
// ---------------------------------------------------------------------------

function checkP5(): void {
  const distDir = resolve(repoRoot, "pkg/ts-bun-client/dist");
  if (!existsSync(distDir)) return; // dist not built — skip silently

  const jsonFiles = walkFiles(
    distDir,
    (f) => f.endsWith(".json"),
    () => false, // never prune inside dist/
  );

  for (const jsonPath of jsonFiles) {
    let parsed: Record<string, unknown>;
    try {
      parsed = JSON.parse(readFileSync(jsonPath, "utf8")) as Record<string, unknown>;
    } catch {
      continue; // not valid JSON — not an authoritative site
    }

    if ("version" in parsed) {
      addViolation(
        jsonPath,
        "version",
        `dist artifact ${relative(repoRoot, jsonPath)} contains a top-level "version" field. ` +
          `dist/ files must be derived from pkg/ts-bun-client/package.json at build time (SR-16).`,
      );
    }
  }
}

// ---------------------------------------------------------------------------
// Run all pattern checks
// ---------------------------------------------------------------------------

checkP1();
checkP2();
checkP3();
checkP4();
checkP5();

// ---------------------------------------------------------------------------
// Report
// ---------------------------------------------------------------------------

if (violations.length > 0) {
  for (const v of violations) {
    process.stderr.write(JSON.stringify(v) + "\n");
  }
  process.exit(1);
}

// exit 0, silent
