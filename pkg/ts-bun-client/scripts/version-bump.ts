/**
 * version-bump.ts — rewrites version-stamp sites for publishing.
 *
 * Usage:
 *   bun run scripts/version-bump.ts --version X.Y.Z [--target <selector>] ...
 *
 * Selectors (--target flag, repeatable):
 *   umbrella-version   — umbrella package.json::version
 *   platform-version   — both platform package.json::version fields
 *   skill-frontmatter  — skills/install-agent-director/SKILL.md frontmatter version:
 *   opt-deps           — umbrella package.json::optionalDependencies file: → ^X.Y.Z
 *
 * When --target is omitted, all four selectors run in the canonical order:
 *   platform-version → umbrella-version → opt-deps → skill-frontmatter
 *
 * ─── Local development vs. publish-time flow ──────────────────────────────
 *
 * During local development the two optional sub-packages are wired via
 * `file:` paths so `bun install` resolves them from the workspace:
 *
 *   "@agent-director/linux-x64":  "file:./platforms/linux-x64"
 *   "@agent-director/darwin-arm64": "file:./platforms/darwin-arm64"
 *
 * Before publishing to npm, CI must:
 *   1. Build each sub-package binary for its target platform.
 *   2. Run `bun run version-bump-publish --version X.Y.Z` to stamp all
 *      version-stamp sites (umbrella, platforms, skill SKILL.md, opt-deps).
 *   3. Publish the two sub-packages first (`npm publish` in each platforms/* subdir).
 *   4. Publish the top-level package.
 *
 * All targets are idempotent: if a file is already at target version the
 * write is skipped and "already at <version> — skipped" is logged.
 *
 * Path resolution works from both source tree and stage-dir copy
 * (both preserve the same relative layout: scriptDir is always under
 * <root>/pkg/ts-bun-client/scripts/).
 * ─────────────────────────────────────────────────────────────────────────
 */

import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Parse --version argument
// ---------------------------------------------------------------------------

const args = process.argv.slice(2);

const versionIdx = args.indexOf("--version");
if (versionIdx === -1 || !args[versionIdx + 1]) {
  console.error(
    "Usage: bun run scripts/version-bump.ts --version X.Y.Z [--target <selector>...]"
  );
  process.exit(1);
}
const version = args[versionIdx + 1];

// Validate: must be X.Y.Z with optional leading zeros.
if (!/^\d+\.\d+\.\d+$/.test(version)) {
  console.error(
    `Error: version "${version}" is not valid semver (expected X.Y.Z)`
  );
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Parse --target flags (repeatable)
// ---------------------------------------------------------------------------

const VALID_SELECTORS = [
  "umbrella-version",
  "skill-frontmatter",
] as const;
type Selector = (typeof VALID_SELECTORS)[number];

const targets: Selector[] = [];
for (let i = 0; i < args.length; i++) {
  if (args[i] === "--target") {
    const sel = args[i + 1];
    if (!sel) {
      console.error("Error: --target requires a selector argument");
      process.exit(1);
    }
    if (!(VALID_SELECTORS as readonly string[]).includes(sel)) {
      console.error(
        `Error: unknown --target "${sel}". Valid selectors: ${VALID_SELECTORS.join(", ")}`
      );
      process.exit(1);
    }
    targets.push(sel as Selector);
    i++;
  }
}

// Canonical run-all order: umbrella first, then skill-frontmatter (the
// most structurally sensitive operation). After b.ue3 / Epic 4 the
// vendored-binary platforms/ tree is gone so platform-version and
// opt-deps are no longer needed.
const ALL_SELECTORS: Selector[] = [
  "umbrella-version",
  "skill-frontmatter",
];

// When no --target is given, run all selectors in canonical order.
const selectedTargets: Selector[] =
  targets.length > 0 ? targets : [...ALL_SELECTORS];

// ---------------------------------------------------------------------------
// Path resolution — works from both source tree and stage-dir copy
// ---------------------------------------------------------------------------

const scriptDir = dirname(fileURLToPath(import.meta.url));

interface RepoPaths {
  /** pkg/ts-bun-client/package.json */
  umbrellaJson: string;
  /** [skills/install-agent-director/SKILL.md] */
  skillMds: [string];
}

function resolveRepoPaths(dir: string): RepoPaths {
  return {
    umbrellaJson: resolve(dir, "../package.json"),
    skillMds: [
      resolve(dir, "../../../skills/install-agent-director/SKILL.md"),
    ],
  };
}

const paths = resolveRepoPaths(scriptDir);

// ---------------------------------------------------------------------------
// Target: umbrella-version
// ---------------------------------------------------------------------------

function bumpUmbrellaVersion(pkgPath: string, ver: string): void {
  const raw = readFileSync(pkgPath, "utf8");
  const pkg = JSON.parse(raw) as { version?: string; [k: string]: unknown };
  if (pkg.version === ver) {
    console.log(`version-bump [umbrella-version]: already at ${ver} — skipped`);
    return;
  }
  pkg.version = ver;
  writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + "\n", "utf8");
  console.log(
    `version-bump [umbrella-version]: set version=${ver} in ${pkgPath}`
  );
}

// ---------------------------------------------------------------------------
// Target: skill-frontmatter
// ---------------------------------------------------------------------------

function bumpSkillFrontmatter(skillPaths: [string], ver: string): void {
  const [skillMdPath] = skillPaths;
  const raw = readFileSync(skillMdPath, "utf8");
  const lines = raw.split("\n");

  // File must begin with the YAML frontmatter opening delimiter.
  if (lines[0] !== "---") {
    console.error(
      `version-bump [skill-frontmatter]: ${skillMdPath}: does not begin with '---' (frontmatter required)`
    );
    process.exit(1);
  }

  // Find the closing '---' (search from line 1 to skip the opener).
  const closeIdx = lines.indexOf("---", 1);
  if (closeIdx === -1) {
    console.error(
      `version-bump [skill-frontmatter]: ${skillMdPath}: no closing '---' found`
    );
    process.exit(1);
  }

  // Frontmatter block is lines[1 .. closeIdx-1].
  const frontmatter = lines.slice(1, closeIdx);

  // Collect indices of version: lines within the frontmatter only.
  const versionLineIndices: number[] = [];
  for (let i = 0; i < frontmatter.length; i++) {
    if (/^version:\s*/.test(frontmatter[i])) {
      versionLineIndices.push(i);
    }
  }

  if (versionLineIndices.length === 0) {
    console.error(
      `version-bump [skill-frontmatter]: ${skillMdPath}: no version line in frontmatter`
    );
    process.exit(1);
  }
  if (versionLineIndices.length > 1) {
    console.error(
      `version-bump [skill-frontmatter]: ${skillMdPath}: ${versionLineIndices.length} version lines in frontmatter (expected 1)`
    );
    process.exit(1);
  }

  const fmIdx = versionLineIndices[0];
  const currentLine = frontmatter[fmIdx];

  // Parse current value — strip optional surrounding quotes for comparison.
  const currentMatch = currentLine.match(/^version:\s*(.+)$/);
  const rawVal = currentMatch ? currentMatch[1].trim() : "";
  const currentVal = rawVal.replace(/^["']|["']$/g, "");

  // Idempotence: skip write if already at target version.
  if (currentVal === ver) {
    console.log(
      `version-bump [skill-frontmatter]: already at ${ver} — skipped`
    );
    return;
  }

  // Replace the version line. Write unquoted (canonical YAML scalar form).
  frontmatter[fmIdx] = `version: ${ver}`;

  // Reassemble: opening --- + (mutated) frontmatter + closing --- + body.
  // lines.slice(closeIdx) starts with the closing '---' then the body.
  const updated = ["---", ...frontmatter, ...lines.slice(closeIdx)].join("\n");
  writeFileSync(skillMdPath, updated, "utf8");
  console.log(
    `version-bump [skill-frontmatter]: set version=${ver} in ${skillMdPath}`
  );
}

// ---------------------------------------------------------------------------
// Path validation — all target paths must exist before any mutation runs
// ---------------------------------------------------------------------------

function requiredPaths(sel: Selector): string[] {
  switch (sel) {
    case "umbrella-version": return [paths.umbrellaJson];
    case "skill-frontmatter": return [...paths.skillMds];
  }
}

const missingPaths: string[] = [];
for (const sel of selectedTargets) {
  for (const p of requiredPaths(sel)) {
    if (!existsSync(p)) {
      missingPaths.push(`[${sel}] ${p}`);
    }
  }
}
if (missingPaths.length > 0) {
  for (const m of missingPaths) {
    console.error(`version-bump: missing required file: ${m}`);
  }
  process.exit(1);
}

// ---------------------------------------------------------------------------
// Dispatch
// ---------------------------------------------------------------------------

for (const target of selectedTargets) {
  switch (target) {
    case "umbrella-version":
      bumpUmbrellaVersion(paths.umbrellaJson, version);
      break;
    case "skill-frontmatter":
      bumpSkillFrontmatter(paths.skillMds, version);
      break;
  }
}
