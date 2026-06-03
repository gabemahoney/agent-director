/**
 * version-bump.test.ts — tests for scripts/version-bump.ts.
 *
 * Uses a staging-tree mirror pattern: makeStagingTree() builds a temp dir
 * that mirrors the relative layout the script expects (script lives at
 * <root>/pkg/ts-bun-client/scripts/version-bump.ts; SKILL.md at
 * <root>/skills/install-agent-director/SKILL.md; the umbrella package.json
 * under <root>/pkg/ts-bun-client/). The script is copied from the real
 * source tree so import.meta.url resolves within the staging root — the same
 * path-resolution model used in production (release.sh cp-a mirrors the
 * layout before invoking the script).
 *
 * Live targets exercised here:
 *   - umbrella-version  → umbrella package.json::version
 *   - skill-frontmatter → skills/install-agent-director/SKILL.md frontmatter version:
 *
 * Staging layout:
 *   <root>/
 *     pkg/ts-bun-client/
 *       scripts/version-bump.ts    ← copied from real source
 *       package.json               ← umbrella, seeded
 *     skills/install-agent-director/SKILL.md
 */

import { test, expect, describe } from "bun:test";
import {
  mkdirSync,
  writeFileSync,
  mkdtempSync,
  rmSync,
  readFileSync,
  cpSync,
} from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const PKG_DIR = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const REAL_SCRIPT = join(PKG_DIR, "scripts", "version-bump.ts");

// ---------------------------------------------------------------------------
// Seed & target constants
// ---------------------------------------------------------------------------

const SEED_VERSION = "0.1.0";
const TARGET_VERSION = "9.9.9";

// ---------------------------------------------------------------------------
// SKILL.md content factory
// ---------------------------------------------------------------------------

/**
 * Build a SKILL.md string.
 * @param frontmatterVersion - version: value to embed in the frontmatter block.
 * @param extraBodyLine - optional extra line to inject into the body (used for
 *   OTQ-2 body-coexists / body-only cases).
 */
function makeSkillMd(frontmatterVersion: string, extraBodyLine?: string): string {
  const body = extraBodyLine
    ? `Body content.\n${extraBodyLine}\nEnd of body.\n`
    : `Body content.\nEnd of body.\n`;
  return `---\nname: install-agent-director\nversion: ${frontmatterVersion}\ndescription: Install agent-director\n---\n${body}`;
}

// ---------------------------------------------------------------------------
// Staging tree factory
// ---------------------------------------------------------------------------

interface StagingTree {
  /** Temp directory root. */
  root: string;
  /** Absolute path to the copied version-bump.ts script. */
  scriptPath: string;
  /** Absolute path to the umbrella package.json. */
  umbrellaPkgPath: string;
  /** Absolute path to skills/install-agent-director/SKILL.md. */
  skillMdPath: string;
  /** Remove the temp directory. Best-effort; never throws. */
  cleanup(): void;
}

/**
 * Build a minimal staging tree that mirrors the layout version-bump.ts
 * expects. Copies the real script so import.meta.url resolves relative to
 * the staging root.
 */
function makeStagingTree(opts: { skillMdContent?: string } = {}): StagingTree {
  const root = mkdtempSync(join(import.meta.dir, ".tmp-vbump-"));

  const scriptsDir = join(root, "pkg", "ts-bun-client", "scripts");
  const pkgDir = join(root, "pkg", "ts-bun-client");
  const skillDir = join(root, "skills", "install-agent-director");

  for (const d of [scriptsDir, skillDir]) {
    mkdirSync(d, { recursive: true });
  }

  // Copy the script so the script's import.meta.url resolves within the tree.
  const scriptPath = join(scriptsDir, "version-bump.ts");
  cpSync(REAL_SCRIPT, scriptPath);

  const umbrellaPkgPath = join(pkgDir, "package.json");
  writeFileSync(
    umbrellaPkgPath,
    JSON.stringify(
      {
        name: "agent-director",
        version: SEED_VERSION,
      },
      null,
      2
    ) + "\n",
    "utf8"
  );

  const skillMdPath = join(skillDir, "SKILL.md");
  writeFileSync(
    skillMdPath,
    opts.skillMdContent ?? makeSkillMd(SEED_VERSION),
    "utf8"
  );

  return {
    root,
    scriptPath,
    umbrellaPkgPath,
    skillMdPath,
    cleanup() {
      try {
        rmSync(root, { recursive: true, force: true });
      } catch {
        /* best-effort */
      }
    },
  };
}

// ---------------------------------------------------------------------------
// Invocation helper
// ---------------------------------------------------------------------------

interface RunResult {
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

/** Invoke the version-bump script directly by path (no cwd needed). */
function runBump(scriptPath: string, args: string[]): RunResult {
  const result = Bun.spawnSync(["bun", "run", scriptPath, ...args], {
    stdout: "pipe",
    stderr: "pipe",
  });
  return {
    exitCode: result.exitCode,
    stdout: result.stdout.toString(),
    stderr: result.stderr.toString(),
  };
}

// ---------------------------------------------------------------------------
// File-read helpers
// ---------------------------------------------------------------------------

function readPkgVersion(pkgPath: string): string {
  return (JSON.parse(readFileSync(pkgPath, "utf8")) as { version: string }).version;
}

/**
 * Extract the version: value from the SKILL.md frontmatter block only.
 * Errors if no frontmatter version is found.
 */
function readSkillFrontmatterVersion(skillMdPath: string): string {
  const lines = readFileSync(skillMdPath, "utf8").split("\n");
  let inFrontmatter = false;
  for (const line of lines) {
    if (line.trim() === "---") {
      if (!inFrontmatter) {
        inFrontmatter = true;
        continue;
      }
      break; // closing ---
    }
    if (inFrontmatter) {
      const m = line.match(/^version:\s*(.+)$/);
      if (m) return m[1].trim();
    }
  }
  throw new Error(`No frontmatter version found in ${skillMdPath}`);
}

// ---------------------------------------------------------------------------
// Isolation-assertion helper
// ---------------------------------------------------------------------------

type Selector = "umbrella-version" | "skill-frontmatter";

/**
 * Assert that every site NOT covered by `activeSelector` is still at the
 * seeded value (i.e., untouched by a per-selector run).
 */
function assertOtherSitesUntouched(tree: StagingTree, activeSelector: Selector): void {
  if (activeSelector !== "umbrella-version") {
    expect(readPkgVersion(tree.umbrellaPkgPath)).toBe(SEED_VERSION);
  }
  if (activeSelector !== "skill-frontmatter") {
    expect(readSkillFrontmatterVersion(tree.skillMdPath)).toBe(SEED_VERSION);
  }
}

// ---------------------------------------------------------------------------
// 1. Per-selector isolation (parametrized × 2)
// ---------------------------------------------------------------------------

const ALL_SELECTORS: Selector[] = [
  "umbrella-version",
  "skill-frontmatter",
];

describe("version-bump --target isolation", () => {
  for (const selector of ALL_SELECTORS) {
    test(`--target=${selector}: updates only the targeted site(s), leaves others untouched`, () => {
      const tree = makeStagingTree();
      try {
        const r = runBump(tree.scriptPath, [
          "--version", TARGET_VERSION,
          "--target", selector,
        ]);
        expect(r.exitCode).toBe(0);

        // Assert the targeted site(s) were updated.
        if (selector === "umbrella-version") {
          expect(readPkgVersion(tree.umbrellaPkgPath)).toBe(TARGET_VERSION);
        } else if (selector === "skill-frontmatter") {
          expect(readSkillFrontmatterVersion(tree.skillMdPath)).toBe(TARGET_VERSION);
        }

        // Assert every other site is untouched.
        assertOtherSitesUntouched(tree, selector);
      } finally {
        tree.cleanup();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// 2. Omitted --target runs all live mutation sites
// ---------------------------------------------------------------------------

describe("version-bump (no --target): updates all live sites", () => {
  test("all sites reach the target version in a single invocation", () => {
    const tree = makeStagingTree();
    try {
      const r = runBump(tree.scriptPath, ["--version", TARGET_VERSION]);
      expect(r.exitCode).toBe(0);

      expect(readPkgVersion(tree.umbrellaPkgPath)).toBe(TARGET_VERSION);
      expect(readSkillFrontmatterVersion(tree.skillMdPath)).toBe(TARGET_VERSION);
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 3. Idempotence (parametrized × 3: default + 2 selectors)
// ---------------------------------------------------------------------------

const IDEMPOTENCE_CASES: Array<{ label: string; extraArgs: string[] }> = [
  { label: "default (all sites)", extraArgs: [] },
  { label: "--target=umbrella-version", extraArgs: ["--target", "umbrella-version"] },
  { label: "--target=skill-frontmatter", extraArgs: ["--target", "skill-frontmatter"] },
];

describe("version-bump idempotence", () => {
  for (const { label, extraArgs } of IDEMPOTENCE_CASES) {
    test(`${label}: second run with same version writes nothing`, () => {
      const tree = makeStagingTree();
      try {
        const baseArgs = ["--version", TARGET_VERSION, ...extraArgs];

        // First run must succeed.
        const r1 = runBump(tree.scriptPath, baseArgs);
        expect(r1.exitCode).toBe(0);

        // Snapshot file contents after first run.
        const snap = {
          umbrella: readFileSync(tree.umbrellaPkgPath, "utf8"),
          skill: readFileSync(tree.skillMdPath, "utf8"),
        };

        // Second run must also succeed.
        const r2 = runBump(tree.scriptPath, baseArgs);
        expect(r2.exitCode).toBe(0);

        // All files must be byte-identical to the post-first-run snapshot.
        expect(readFileSync(tree.umbrellaPkgPath, "utf8")).toBe(snap.umbrella);
        expect(readFileSync(tree.skillMdPath, "utf8")).toBe(snap.skill);

        // Second-run output must acknowledge the no-op with a "skipped" message.
        expect(r2.stdout + r2.stderr).toMatch(/skipped/i);
      } finally {
        tree.cleanup();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// 4. OTQ-2 SKILL.md frontmatter robustness (parametrized × 4)
// ---------------------------------------------------------------------------

describe("version-bump OTQ-2: SKILL.md frontmatter robustness", () => {
  test("zero version: lines in frontmatter → exits non-zero with descriptive error", () => {
    const content = `---\nname: install-agent-director\ndescription: No version here\n---\nBody.\n`;
    const tree = makeStagingTree({ skillMdContent: content });
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "skill-frontmatter",
      ]);
      expect(r.exitCode).not.toBe(0);
      // Error must mention the fault (no version line).
      expect(r.stderr + r.stdout).toMatch(/version/i);
    } finally {
      tree.cleanup();
    }
  });

  test("multiple version: lines in frontmatter → exits non-zero with descriptive error", () => {
    const content = `---\nname: install-agent-director\nversion: 0.1.0\nversion: 0.2.0\n---\nBody.\n`;
    const tree = makeStagingTree({ skillMdContent: content });
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "skill-frontmatter",
      ]);
      expect(r.exitCode).not.toBe(0);
      // Error must mention ambiguity (multiple / count).
      expect(r.stderr + r.stdout).toMatch(/version/i);
    } finally {
      tree.cleanup();
    }
  });

  test("body version: line coexists with frontmatter version: → only frontmatter rewritten, body byte-identical", () => {
    const bodyLine = "version: this line lives in the body, not the frontmatter";
    const content = makeSkillMd(SEED_VERSION, bodyLine);
    const tree = makeStagingTree({ skillMdContent: content });
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "skill-frontmatter",
      ]);
      expect(r.exitCode).toBe(0);

      const updated = readFileSync(tree.skillMdPath, "utf8");

      // Frontmatter version must be updated.
      expect(readSkillFrontmatterVersion(tree.skillMdPath)).toBe(TARGET_VERSION);

      // Body version: line must be byte-identical (untouched).
      expect(updated).toContain(bodyLine);
      // Exactly one occurrence of the target version should appear (in frontmatter only).
      expect(updated.split(`version: ${TARGET_VERSION}`).length - 1).toBe(1);
    } finally {
      tree.cleanup();
    }
  });

  test("body-only version: line with no frontmatter version → exits non-zero", () => {
    // Frontmatter has zero version: lines; body has one.
    const content =
      `---\nname: install-agent-director\ndescription: No frontmatter version\n---\n` +
      `Body content.\nversion: only here in body\n`;
    const tree = makeStagingTree({ skillMdContent: content });
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "skill-frontmatter",
      ]);
      expect(r.exitCode).not.toBe(0);
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 5. Unknown --target selector → exits non-zero with usage-style message
// ---------------------------------------------------------------------------

describe("version-bump unknown --target selector", () => {
  test("exits non-zero and lists valid selectors in the error output", () => {
    const tree = makeStagingTree();
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "totally-invalid-xyz",
      ]);
      expect(r.exitCode).not.toBe(0);

      const output = r.stderr + r.stdout;
      // Must mention the bad value.
      expect(output).toContain("totally-invalid-xyz");
      // Must name at least one valid selector (usage message).
      expect(output).toMatch(/umbrella-version|skill-frontmatter/);
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 6. Missing required file → exits non-zero and names the missing path
//    (S3 AC: "Missing target path → exit non-zero with a clear message naming the missing file")
// ---------------------------------------------------------------------------

describe("version-bump: missing required target file", () => {
  test("absent SKILL.md → exits non-zero with error naming the missing file", () => {
    const tree = makeStagingTree();
    // Remove the skill file before invoking the script.
    rmSync(tree.skillMdPath);
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "skill-frontmatter",
      ]);
      expect(r.exitCode).not.toBe(0);
      expect(r.stderr + r.stdout).toContain("SKILL.md");
    } finally {
      tree.cleanup();
    }
  });
});
