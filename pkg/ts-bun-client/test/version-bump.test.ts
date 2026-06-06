/**
 * version-bump.test.ts — tests for scripts/version-bump.ts.
 *
 * Uses a staging-tree mirror pattern: makeStagingTree() builds a temp dir
 * that mirrors the relative layout the script expects (script lives at
 * <root>/pkg/ts-bun-client/scripts/version-bump.ts; the umbrella package.json
 * under <root>/pkg/ts-bun-client/). The script is copied from the real
 * source tree so import.meta.url resolves within the staging root — the same
 * path-resolution model used in production (release.sh cp-a mirrors the
 * layout before invoking the script).
 *
 * Live targets exercised here:
 *   - umbrella-version  → umbrella package.json::version
 *
 * Staging layout:
 *   <root>/
 *     pkg/ts-bun-client/
 *       scripts/version-bump.ts    ← copied from real source
 *       package.json               ← umbrella, seeded
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
// Staging tree factory
// ---------------------------------------------------------------------------

interface StagingTree {
  /** Temp directory root. */
  root: string;
  /** Absolute path to the copied version-bump.ts script. */
  scriptPath: string;
  /** Absolute path to the umbrella package.json. */
  umbrellaPkgPath: string;
  /** Remove the temp directory. Best-effort; never throws. */
  cleanup(): void;
}

/**
 * Build a minimal staging tree that mirrors the layout version-bump.ts
 * expects. Copies the real script so import.meta.url resolves relative to
 * the staging root.
 */
function makeStagingTree(): StagingTree {
  const root = mkdtempSync(join(import.meta.dir, ".tmp-vbump-"));

  const scriptsDir = join(root, "pkg", "ts-bun-client", "scripts");
  const pkgDir = join(root, "pkg", "ts-bun-client");

  mkdirSync(scriptsDir, { recursive: true });

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

  return {
    root,
    scriptPath,
    umbrellaPkgPath,
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

// ---------------------------------------------------------------------------
// 1. Per-selector isolation
// ---------------------------------------------------------------------------

describe("version-bump --target isolation", () => {
  test("--target=umbrella-version: updates only the targeted site(s), leaves others untouched", () => {
    const tree = makeStagingTree();
    try {
      const r = runBump(tree.scriptPath, [
        "--version", TARGET_VERSION,
        "--target", "umbrella-version",
      ]);
      expect(r.exitCode).toBe(0);
      expect(readPkgVersion(tree.umbrellaPkgPath)).toBe(TARGET_VERSION);
    } finally {
      tree.cleanup();
    }
  });
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
    } finally {
      tree.cleanup();
    }
  });
});

// ---------------------------------------------------------------------------
// 3. Idempotence (parametrized × 2: default + umbrella-version selector)
// ---------------------------------------------------------------------------

const IDEMPOTENCE_CASES: Array<{ label: string; extraArgs: string[] }> = [
  { label: "default (all sites)", extraArgs: [] },
  { label: "--target=umbrella-version", extraArgs: ["--target", "umbrella-version"] },
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
        };

        // Second run must also succeed.
        const r2 = runBump(tree.scriptPath, baseArgs);
        expect(r2.exitCode).toBe(0);

        // All files must be byte-identical to the post-first-run snapshot.
        expect(readFileSync(tree.umbrellaPkgPath, "utf8")).toBe(snap.umbrella);

        // Second-run output must acknowledge the no-op with a "skipped" message.
        expect(r2.stdout + r2.stderr).toMatch(/skipped/i);
      } finally {
        tree.cleanup();
      }
    });
  }
});

// ---------------------------------------------------------------------------
// 4. Unknown --target selector → exits non-zero with usage-style message
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
      expect(output).toMatch(/umbrella-version/);
    } finally {
      tree.cleanup();
    }
  });
});
