/**
 * postinstall.ts — runs after `bun add agent-director` on a supported host.
 *
 * Copies the bundled install skill body from
 *   node_modules/agent-director/skills/install-agent-director/
 * into
 *   ${HOME}/.claude/skills/install-agent-director/
 * via an atomic-rename with a three-way decision
 * (identical-noop / older-or-absent-overwrite-with-backup / newer-leave-alone).
 *
 * Authoritative contract: SRD `t1.fg3.7i` §SR-1 (the postinstall surface),
 * Plan Bee `b.3d3` Epic 1 (postinstall-skill-copy).
 *
 * Constraints:
 *  - Bun runtime only. Pure node stdlib imports (SR-1.1) — no third-party
 *    deps, no import from `src/` (the FFI native binary may not be linked
 *    into `node_modules` at postinstall time).
 *  - Never writes outside `${HOME}/.claude/skills/install-agent-director/`
 *    (plus its sibling tmp + backup paths). Never touches PATH, the state
 *    DB, ~/.claude/settings.json, or install.sh.
 *
 * This file lands the helpers (T2) — source/destination resolution,
 * semver compare, tree-hash, frontmatter `version:` extractor,
 * AD_POSTINSTALL_VERBOSE reader, and a stub `main()`. The three-way
 * branches and atomic-write algorithm land in T3; output budget in T4;
 * the platform refusal lands in Epic 2 T2.
 */

import { createHash } from "node:crypto";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { homedir } from "node:os";
import { dirname, join, relative, resolve } from "node:path";
import { fileURLToPath } from "node:url";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PACKAGE_NAME = "agent-director";
const SKILL_REL_PATH = join("skills", "install-agent-director");
const DEST_REL_FROM_HOME = join(".claude", "skills", "install-agent-director");

// ---------------------------------------------------------------------------
// AD_POSTINSTALL_VERBOSE env reader (SR-1.6)
// Truthy iff "1", "true", or "yes" (case-insensitive). Anything else
// (including empty/unset) is falsy.
// ---------------------------------------------------------------------------

export function isVerbose(env: NodeJS.ProcessEnv = process.env): boolean {
  const raw = env["AD_POSTINSTALL_VERBOSE"];
  if (raw === undefined) return false;
  const v = raw.trim().toLowerCase();
  return v === "1" || v === "true" || v === "yes";
}

// ---------------------------------------------------------------------------
// Source resolution (SR-1.2)
// Walk up from import.meta.url to the package root, then descend into
// skills/install-agent-director/. The package root is recognized by a
// sibling package.json whose `name` is "agent-director".
// ---------------------------------------------------------------------------

export function resolvePackageRoot(startFile: string): string {
  let dir = dirname(resolve(startFile));
  // Bounded walk so a misplaced script never traverses to "/".
  for (let i = 0; i < 32; i++) {
    const pkgJson = join(dir, "package.json");
    try {
      const raw = readFileSync(pkgJson, "utf8");
      const parsed = JSON.parse(raw) as { name?: string };
      if (parsed.name === PACKAGE_NAME) return dir;
    } catch {
      // Either missing package.json or not the umbrella — keep walking.
    }
    const parent = dirname(dir);
    if (parent === dir) break;
    dir = parent;
  }
  throw new Error(
    `postinstall: could not locate ${PACKAGE_NAME} package.json walking up from ${startFile}`,
  );
}

export function resolveSourceSkillDir(packageRoot: string): string {
  return join(packageRoot, SKILL_REL_PATH);
}

export function resolveDestSkillDir(): string {
  return join(homedir(), DEST_REL_FROM_HOME);
}

// ---------------------------------------------------------------------------
// Frontmatter `version:` extractor (SR-1.3)
// Reads a SKILL.md, parses the `---`-delimited YAML frontmatter block, and
// returns the `version:` value as a string. Missing file, missing
// frontmatter, or missing `version` key → "0.0.0" (the older-or-absent
// fallback).
// ---------------------------------------------------------------------------

export function readFrontmatterVersion(skillMdPath: string): string {
  let raw: string;
  try {
    raw = readFileSync(skillMdPath, "utf8");
  } catch {
    return "0.0.0";
  }
  // Frontmatter must be the very first thing in the file. The opening fence
  // is "---" on its own line; the closing fence is the next "---" on its
  // own line.
  const lines = raw.split(/\r?\n/);
  if (lines[0] !== "---") return "0.0.0";
  for (let i = 1; i < lines.length; i++) {
    const line = lines[i] ?? "";
    if (line === "---") break;
    // Flat YAML only: `key: value` per line.
    const m = /^version:\s*(.+?)\s*$/.exec(line);
    if (m && m[1] !== undefined) {
      // Strip surrounding quotes if any (YAML allows both).
      return m[1].replace(/^["']|["']$/g, "");
    }
  }
  return "0.0.0";
}

// ---------------------------------------------------------------------------
// Semver comparator (SR-1.3, semver §11)
// Parses M.m.p[-prerelease][+build]. Returns -1/0/+1. Inputs that fail to
// parse as semver are treated as "0.0.0".
// ---------------------------------------------------------------------------

type ParsedSemver = {
  major: number;
  minor: number;
  patch: number;
  prerelease: string[]; // empty array means no prerelease (higher precedence)
};

function parseSemver(s: string): ParsedSemver {
  // Strip build metadata (after "+") — ignored for precedence per semver §10.
  const noBuild = s.split("+", 1)[0] ?? s;
  const dashIdx = noBuild.indexOf("-");
  const core = dashIdx >= 0 ? noBuild.slice(0, dashIdx) : noBuild;
  const pre = dashIdx >= 0 ? noBuild.slice(dashIdx + 1) : "";
  const m = /^(\d+)\.(\d+)\.(\d+)$/.exec(core);
  if (!m) return { major: 0, minor: 0, patch: 0, prerelease: [] };
  const prerelease = pre === "" ? [] : pre.split(".");
  // Validate prerelease identifiers — alphanumerics + hyphens only per §9.
  for (const id of prerelease) {
    if (id === "" || !/^[0-9A-Za-z-]+$/.test(id)) {
      return { major: 0, minor: 0, patch: 0, prerelease: [] };
    }
  }
  return {
    major: Number(m[1]),
    minor: Number(m[2]),
    patch: Number(m[3]),
    prerelease,
  };
}

function compareIds(a: string, b: string): number {
  const aNum = /^\d+$/.test(a);
  const bNum = /^\d+$/.test(b);
  if (aNum && bNum) {
    const an = Number(a);
    const bn = Number(b);
    return an < bn ? -1 : an > bn ? 1 : 0;
  }
  // Numeric identifiers have lower precedence than non-numeric (§11).
  if (aNum) return -1;
  if (bNum) return 1;
  return a < b ? -1 : a > b ? 1 : 0;
}

export function compareSemver(a: string, b: string): -1 | 0 | 1 {
  const pa = parseSemver(a);
  const pb = parseSemver(b);
  if (pa.major !== pb.major) return pa.major < pb.major ? -1 : 1;
  if (pa.minor !== pb.minor) return pa.minor < pb.minor ? -1 : 1;
  if (pa.patch !== pb.patch) return pa.patch < pb.patch ? -1 : 1;
  // §11.4: a version with prerelease has lower precedence than one without.
  const aPre = pa.prerelease.length > 0;
  const bPre = pb.prerelease.length > 0;
  if (aPre && !bPre) return -1;
  if (!aPre && bPre) return 1;
  if (!aPre && !bPre) return 0;
  // Compare prerelease identifiers left-to-right.
  const len = Math.min(pa.prerelease.length, pb.prerelease.length);
  for (let i = 0; i < len; i++) {
    const cmp = compareIds(pa.prerelease[i]!, pb.prerelease[i]!);
    if (cmp !== 0) return cmp < 0 ? -1 : 1;
  }
  // Longer prerelease wins (§11.4.4).
  if (pa.prerelease.length < pb.prerelease.length) return -1;
  if (pa.prerelease.length > pb.prerelease.length) return 1;
  return 0;
}

// ---------------------------------------------------------------------------
// Tree-hash (SR-1.4)
// SHA-256 of `<rel-path>\n<sha256-of-file-bytes>\n` concatenated in sorted
// order. Stable across operator chmod/chown. Cheaper than mtime walks.
// Directories that don't exist hash to the empty string's SHA-256 of the
// canonical empty manifest — they cannot match a non-empty source, so the
// caller's "destination missing" branch never collides with identical.
// ---------------------------------------------------------------------------

function listFilesRecursive(root: string): string[] {
  const out: string[] = [];
  const walk = (dir: string): void => {
    let entries;
    try {
      entries = readdirSync(dir, { withFileTypes: true });
    } catch {
      return;
    }
    for (const ent of entries) {
      const full = join(dir, ent.name);
      if (ent.isDirectory()) walk(full);
      else if (ent.isFile()) out.push(full);
      // Symlinks in the *source* are followed (they're package content); the
      // copy step in T3 materializes them as regular files. For tree-hashing
      // we only ever read source/destination trees that are themselves
      // regular files post-copy, so symlinks here would be source-side only.
      else if (ent.isSymbolicLink()) {
        try {
          const st = statSync(full);
          if (st.isFile()) out.push(full);
        } catch {
          // dangling symlink — skip
        }
      }
    }
  };
  walk(root);
  return out;
}

export function treeHash(root: string): string {
  const files = listFilesRecursive(root).sort();
  const outer = createHash("sha256");
  for (const abs of files) {
    const rel = relative(root, abs);
    const inner = createHash("sha256");
    inner.update(readFileSync(abs));
    outer.update(rel);
    outer.update("\n");
    outer.update(inner.digest("hex"));
    outer.update("\n");
  }
  return outer.digest("hex");
}

// ---------------------------------------------------------------------------
// main() — stub. T3 fills in the three-way branch + atomic-write algorithm.
// ---------------------------------------------------------------------------

function main(): number {
  const verbose = isVerbose();
  if (verbose) {
    process.stdout.write("agent-director: postinstall start\n");
  }
  // T3 will compose the helpers below into the three-way branches.
  const here = fileURLToPath(import.meta.url);
  const packageRoot = resolvePackageRoot(here);
  const sourceDir = resolveSourceSkillDir(packageRoot);
  const destDir = resolveDestSkillDir();
  if (verbose) {
    process.stdout.write(`agent-director: source=${sourceDir}\n`);
    process.stdout.write(`agent-director: dest=${destDir}\n`);
  }
  return 0;
}

// Run if invoked directly (not when imported by a sibling test).
// `import.meta.main` is a Bun-ism; fall back to argv comparison for portability.
const isMain =
  // @ts-expect-error import.meta.main is Bun-specific and may be undefined
  (typeof import.meta.main === "boolean" && import.meta.main) ||
  // Best-effort fallback: invoked as `bun run scripts/postinstall.ts`.
  process.argv[1] !== undefined &&
    resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (isMain) {
  process.exit(main());
}
