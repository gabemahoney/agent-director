/**
 * Publish-guard tests for check-not-placeholder.ts.
 *
 * (a) Guard exits 1 when invoked from a directory whose package.json name
 *     contains "CHANGEME-H3".
 * (b) Guard exits 0 when invoked from a directory whose package.json has a
 *     real (non-placeholder) name.
 * (c) Static assertion: all four real package.json files in the worktree have
 *     a scripts.prepublishOnly entry that invokes check-not-placeholder.
 * (d) Post-H3-resolution invariant: none of the four real package.json files
 *     carries the CHANGEME-H3 placeholder in its name. The guard remains a
 *     forward-going tripwire against re-introducing a placeholder.
 *
 * NOTE on npm publish --dry-run and prepublishOnly:
 * npm has historically skipped pre-publish hooks (prepublishOnly) when run
 * with --dry-run. As of npm v7+ the behaviour varies; in some versions
 * --dry-run suppresses the hooks entirely. The guard is therefore tested
 * directly via Bun.spawnSync (cases a/b above), not via npm publish --dry-run.
 * The actual enforcement relies on: (1) the guard script itself, and (2) CI
 * running a dry-run without --dry-run to verify the hook fires. For the
 * purposes of this test suite, the direct invocation is the authoritative gate.
 */

import { test, expect, describe } from "bun:test";
import { mkdirSync, writeFileSync, mkdtempSync, rmSync } from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";
import { readFileSync } from "node:fs";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const guardScript = resolve(pkgDir, "scripts", "check-not-placeholder.ts");

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a temp dir with a minimal package.json and return the dir path. */
function makeTmpPkg(name: string): string {
  const dir = mkdtempSync(join(import.meta.dir, ".tmp-guard-"));
  writeFileSync(join(dir, "package.json"), JSON.stringify({ name }), "utf8");
  return dir;
}

function cleanTmpPkg(dir: string): void {
  try {
    rmSync(dir, { recursive: true, force: true });
  } catch {
    // best-effort cleanup
  }
}

// ---------------------------------------------------------------------------
// (a) Placeholder name → exit 1
// ---------------------------------------------------------------------------

describe("publish-guard — placeholder name", () => {
  test("exits 1 when package.json name contains CHANGEME-H3", () => {
    const tmpDir = makeTmpPkg("@CHANGEME-H3/foo");
    try {
      const result = Bun.spawnSync(
        ["bun", "run", guardScript],
        { cwd: tmpDir, stderr: "pipe", stdout: "pipe" }
      );
      expect(result.exitCode).toBe(1);
      const stderr = result.stderr.toString();
      expect(stderr).toContain("CHANGEME-H3");
      expect(stderr).toContain("release-blockers.md");
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });
});

// ---------------------------------------------------------------------------
// (b) Real name → exit 0
// ---------------------------------------------------------------------------

describe("publish-guard — real name", () => {
  test("exits 0 when package.json name is a real (non-placeholder) scope", () => {
    const tmpDir = makeTmpPkg("@real-scope/foo");
    try {
      const result = Bun.spawnSync(
        ["bun", "run", guardScript],
        { cwd: tmpDir, stderr: "pipe", stdout: "pipe" }
      );
      expect(result.exitCode).toBe(0);
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });
});

// ---------------------------------------------------------------------------
// (c) Static: all four package.json files wire prepublishOnly to the guard
// (d) Static: none of the four package.json files carries the H3 placeholder
// ---------------------------------------------------------------------------

describe("publish-guard — wiring assertions", () => {
  const packageFiles = [
    resolve(pkgDir, "package.json"),
    resolve(pkgDir, "platforms", "linux-x64", "package.json"),
    resolve(pkgDir, "platforms", "darwin-x64", "package.json"),
    resolve(pkgDir, "platforms", "darwin-arm64", "package.json"),
  ];

  for (const pkgFile of packageFiles) {
    const label = pkgFile.replace(pkgDir + "/", "");

    test(`${label}: scripts.prepublishOnly invokes check-not-placeholder`, () => {
      const pkg = JSON.parse(readFileSync(pkgFile, "utf8")) as {
        scripts?: Record<string, string>;
      };
      const prepublishOnly = pkg.scripts?.prepublishOnly;
      expect(
        prepublishOnly,
        `${label} is missing scripts.prepublishOnly`
      ).toBeDefined();
      expect(
        /check-not-placeholder/.test(prepublishOnly ?? ""),
        `${label} scripts.prepublishOnly does not invoke check-not-placeholder (got: ${prepublishOnly})`
      ).toBe(true);
    });
  }

  for (const pkgFile of packageFiles) {
    const label = pkgFile.replace(pkgDir + "/", "");

    test(`${label}: name no longer contains the CHANGEME-H3 placeholder`, () => {
      const pkg = JSON.parse(readFileSync(pkgFile, "utf8")) as {
        name?: string;
      };
      expect(typeof pkg.name).toBe("string");
      expect(
        (pkg.name ?? "").includes("CHANGEME-H3"),
        `${label} name still contains the CHANGEME-H3 placeholder (got: ${pkg.name})`
      ).toBe(false);
    });
  }
});
