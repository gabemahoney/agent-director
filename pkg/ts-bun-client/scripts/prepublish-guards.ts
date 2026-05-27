/**
 * prepublish-guards.ts — composite prepublishOnly guard for the umbrella
 * `agent-director` package. Aborts any `npm publish` / `bun publish` if any
 * of the four invariants is violated:
 *
 *   0. Placeholder-name guard:
 *      package.json `name` must not match PLACEHOLDER_RE.
 *   1. Version skew (SR-4.1):
 *      umbrella `version` must equal the frontmatter `version:` of
 *      skills/install-agent-director/SKILL.md.
 *   2. os/cpu drift (SR-3.1):
 *      umbrella `os` MUST equal ["linux","darwin"] (exact match including
 *      element order); `cpu` MUST equal ["x64","arm64"].
 *   3. optionalDependencies range (SR-3.3):
 *      both `@agent-director/linux-x64` and `@agent-director/darwin-arm64`
 *      entries MUST equal `^<umbrella.version>` (the caret-pin convention
 *      applied by the version-bump utility).
 *
 * Mode selector: PREPUBLISH_GUARD_MODE
 *   unset/empty   → full four-guard composite (default; umbrella publish)
 *   "subpackage"  → placeholder + version-sanity only (platform sub-packages)
 *   anything else → exit 1 with stderr "unknown PREPUBLISH_GUARD_MODE=<val>"
 *
 * SR-3.3: adding a new placeholder name is a one-line change to PLACEHOLDER_RE.
 *
 * Invoked as: `bun run scripts/prepublish-guards.ts` from the package's
 * directory (npm/bun publish CWDs the package dir before lifecycle scripts).
 *
 * Stdlib-only imports (node:fs, node:path), no third-party deps.
 */

import { readFileSync } from "node:fs";
import { join } from "node:path";

// ---------------------------------------------------------------------------
// Canonical placeholder regex — SR-3.3: one-line change to add new sentinels
// ---------------------------------------------------------------------------

const PLACEHOLDER_RE = /^@?(CHANGEME-H3|TBD)\//;

// ---------------------------------------------------------------------------
// Mode selector
// ---------------------------------------------------------------------------

const GUARD_MODE = process.env.PREPUBLISH_GUARD_MODE ?? "";

if (GUARD_MODE !== "" && GUARD_MODE !== "subpackage") {
  console.error(`prepublish-guards: unknown PREPUBLISH_GUARD_MODE=${GUARD_MODE}`);
  process.exit(1);
}

const cwd = process.cwd();
const pkgPath = join(cwd, "package.json");
const skillMdPath = join(cwd, "..", "..", "skills", "install-agent-director", "SKILL.md");

function fail(msg: string): never {
  console.error(`prepublish-guards: ${msg}`);
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Load package.json
// ---------------------------------------------------------------------------

let pkg: {
  name?: string;
  version?: string;
  os?: unknown;
  cpu?: unknown;
  optionalDependencies?: Record<string, string>;
};
try {
  pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
} catch (err) {
  fail(`failed to read ${pkgPath}: ${err instanceof Error ? err.message : String(err)}`);
}

// ---------------------------------------------------------------------------
// Guard 0 — placeholder name
// ---------------------------------------------------------------------------

if (typeof pkg.name === "string" && PLACEHOLDER_RE.test(pkg.name)) {
  fail(`publish blocked: package.json name contains placeholder (${pkg.name}); see docs/release-blockers.md`);
}

// ---------------------------------------------------------------------------
// Version sanity (prerequisite for Guards 1 and 3)
// ---------------------------------------------------------------------------

if (typeof pkg.version !== "string" || pkg.version.length === 0) {
  fail(`publish blocked: package.json version is missing or empty`);
}
const umbrellaVersion = pkg.version;

// ---------------------------------------------------------------------------
// subpackage mode: placeholder + version-sanity only; skip Guards 1–3
// ---------------------------------------------------------------------------

if (GUARD_MODE === "subpackage") {
  process.exit(0);
}

// ---------------------------------------------------------------------------
// Guard 1 — version skew (SR-4.1)
// ---------------------------------------------------------------------------

function readFrontmatterVersion(path: string): string | null {
  let raw: string;
  try {
    raw = readFileSync(path, "utf8");
  } catch {
    return null;
  }
  const lines = raw.split(/\r?\n/);
  if (lines[0] !== "---") return null;
  for (let i = 1; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line === "---") break;
    const m = /^version:\s*(.+?)\s*$/.exec(line);
    if (m && m[1] !== undefined) {
      return m[1].replace(/^["']|["']$/g, "");
    }
  }
  return null;
}

const skillVersion = readFrontmatterVersion(skillMdPath);
if (skillVersion === null) {
  fail(`publish blocked: SKILL.md frontmatter version: field missing at ${skillMdPath}. Re-run the lockstep version bump.`);
}
if (skillVersion !== umbrellaVersion) {
  fail(`publish blocked: version skew — package.json=${umbrellaVersion}, SKILL.md frontmatter=${skillVersion}. Re-run the lockstep version bump to bring them into sync.`);
}

// ---------------------------------------------------------------------------
// Guard 2 — os/cpu drift (SR-3.1)
// ---------------------------------------------------------------------------

function arrayEquals(actual: unknown, expected: readonly string[]): boolean {
  if (!Array.isArray(actual)) return false;
  if (actual.length !== expected.length) return false;
  for (let i = 0; i < expected.length; i++) {
    if (actual[i] !== expected[i]) return false;
  }
  return true;
}

const expectedOs = ["linux", "darwin"] as const;
const expectedCpu = ["x64", "arm64"] as const;

if (!arrayEquals(pkg.os, expectedOs)) {
  fail(
    `publish blocked: os field drift — expected ${JSON.stringify(expectedOs)}, got ${JSON.stringify(pkg.os)}. The SR-3.1 pin is exact-match (including element order).`,
  );
}
if (!arrayEquals(pkg.cpu, expectedCpu)) {
  fail(
    `publish blocked: cpu field drift — expected ${JSON.stringify(expectedCpu)}, got ${JSON.stringify(pkg.cpu)}. The SR-3.1 pin is exact-match (including element order).`,
  );
}

// ---------------------------------------------------------------------------
// Guard 3 — optionalDependencies range (SR-3.3)
// ---------------------------------------------------------------------------

const optDeps = pkg.optionalDependencies ?? {};
const expectedPin = `^${umbrellaVersion}`;
const subPackages = ["@agent-director/linux-x64", "@agent-director/darwin-arm64"] as const;

for (const subPkg of subPackages) {
  const actual = optDeps[subPkg];
  if (actual === undefined) {
    fail(`publish blocked: optionalDependencies missing ${subPkg}. Re-run the version-bump utility to restore the entry.`);
  }
  if (actual !== expectedPin) {
    fail(
      `publish blocked: optionalDependencies[${subPkg}] = ${actual}, expected ${expectedPin}. ` +
        `The release pipeline must rewrite file: dev-time entries to the caret-pin form before publish; run the version-bump utility against this release tag.`,
    );
  }
}

process.exit(0);
