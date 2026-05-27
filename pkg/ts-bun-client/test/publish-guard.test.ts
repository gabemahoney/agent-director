/**
 * Publish-guard tests for prepublish-guards.ts.
 *
 * (a) Guard exits 1 when package.json name matches PLACEHOLDER_RE.
 * (b) Guard exits 0 for real (non-placeholder) names with valid version.
 * (c) PREPUBLISH_GUARD_MODE=subpackage skips umbrella-only Guards 1–3.
 * (d) Unknown PREPUBLISH_GUARD_MODE → exit 1 with descriptive stderr.
 * (e) Wiring: both platform package.json files invoke prepublish-guards.ts
 *     with PREPUBLISH_GUARD_MODE=subpackage; all three packages have no H3 name.
 * (f) Deletion invariant: check-not-placeholder.ts is gone from disk.
 * (g) Default mode (unset): four-guard composite is active.
 *
 * Tests spawn the real script against temp dirs — no inline mocks.
 *
 * NOTE on PLACEHOLDER_RE shape: /^@?(CHANGEME-H3|TBD)\//
 * "Bare" variants without a trailing slash (CHANGEME-H3-bare, TBD-prefix)
 * do NOT match — the regex requires a '/' after the sentinel. Tests reflect
 * actual behavior, not aspirational wording.
 */

import { test, expect, describe } from "bun:test";
import {
  writeFileSync,
  mkdtempSync,
  rmSync,
  existsSync,
  readFileSync,
} from "node:fs";
import { join, resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const guardScript = resolve(pkgDir, "scripts", "prepublish-guards.ts");
// Two levels up from pkg/ts-bun-client → repo root
const repoRoot = resolve(pkgDir, "..", "..");

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Create a temp dir with a minimal package.json and return the dir path. */
function makeTmpPkg(fields: Record<string, unknown>): string {
  const dir = mkdtempSync(join(import.meta.dir, ".tmp-guard-"));
  writeFileSync(join(dir, "package.json"), JSON.stringify(fields), "utf8");
  return dir;
}

function cleanTmpPkg(dir: string): void {
  try {
    rmSync(dir, { recursive: true, force: true });
  } catch {
    // best-effort cleanup
  }
}

/**
 * Spawn prepublish-guards.ts from cwd.
 * Explicit env: inherits process.env, strips PREPUBLISH_GUARD_MODE, then
 * overlays extraEnv so each test controls the mode precisely.
 */
function runGuard(cwd: string, extraEnv?: Record<string, string>) {
  const env: Record<string, string> = {};
  for (const [k, v] of Object.entries(process.env)) {
    if (v !== undefined) env[k] = v;
  }
  delete env["PREPUBLISH_GUARD_MODE"];
  if (extraEnv) Object.assign(env, extraEnv);
  return Bun.spawnSync(["bun", "run", guardScript], {
    cwd,
    stderr: "pipe",
    stdout: "pipe",
    env,
  });
}

// ---------------------------------------------------------------------------
// (a/b) Parametrized placeholder-regex cases
//
// All cases run in PREPUBLISH_GUARD_MODE=subpackage with a version field so:
//   - reject cases fail fast at Guard 0 (name check)
//   - accept cases exit 0 without needing a real SKILL.md / umbrella structure
//
// CHANGEME-H3-bare and TBD-prefix have no trailing '/' so the regex does NOT
// match them → actual exit is 0, not 1.
// ---------------------------------------------------------------------------

const REGEX_CASES: Array<{
  name: string;
  expectedExit: 0 | 1;
  note?: string;
}> = [
  // Reject — regex matches (sentinel followed by '/')
  { name: "@CHANGEME-H3/foo", expectedExit: 1 },
  { name: "CHANGEME-H3/foo",  expectedExit: 1, note: "unscoped with slash" },
  { name: "@TBD/scope",       expectedExit: 1 },
  { name: "TBD/scope",        expectedExit: 1, note: "unscoped TBD with slash" },
  // Accept — bare sentinel without trailing '/' → regex does NOT match
  { name: "CHANGEME-H3-bare", expectedExit: 0, note: "no '/' → regex does not match" },
  { name: "TBD-prefix",       expectedExit: 0, note: "no '/' → regex does not match" },
  // Clean accepts
  { name: "@agent-director/linux-x64", expectedExit: 0 },
  { name: "@real-scope/foo",           expectedExit: 0 },
  { name: "real-package",              expectedExit: 0 },
];

describe("publish-guard — placeholder regex (parametrized)", () => {
  for (const { name, expectedExit, note } of REGEX_CASES) {
    const label = note ? `${name} (${note})` : name;
    test(`${label} → exit ${expectedExit}`, () => {
      const tmpDir = makeTmpPkg({ name, version: "0.1.0" });
      try {
        const result = runGuard(tmpDir, { PREPUBLISH_GUARD_MODE: "subpackage" });
        expect(result.exitCode).toBe(expectedExit);
        if (expectedExit === 1) {
          expect(result.stderr.toString()).toContain("placeholder");
        }
      } finally {
        cleanTmpPkg(tmpDir);
      }
    });
  }
});

// ---------------------------------------------------------------------------
// (c) PREPUBLISH_GUARD_MODE=subpackage
// ---------------------------------------------------------------------------

describe("publish-guard — subpackage mode", () => {
  test("placeholder name → exit 1", () => {
    const tmpDir = makeTmpPkg({ name: "@CHANGEME-H3/foo" });
    try {
      const result = runGuard(tmpDir, { PREPUBLISH_GUARD_MODE: "subpackage" });
      expect(result.exitCode).toBe(1);
      const stderr = result.stderr.toString();
      expect(stderr).toContain("CHANGEME-H3");
      expect(stderr).toContain("release-blockers.md");
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });

  test("real name + valid version → exit 0", () => {
    const tmpDir = makeTmpPkg({ name: "@agent-director/linux-x64", version: "0.4.0" });
    try {
      const result = runGuard(tmpDir, { PREPUBLISH_GUARD_MODE: "subpackage" });
      expect(result.exitCode).toBe(0);
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });

  test("broken os/cpu/optionalDependencies → exit 0 (umbrella Guards 1–3 are skipped)", () => {
    const tmpDir = makeTmpPkg({
      name: "@agent-director/linux-x64",
      version: "0.4.0",
      os: ["wrong-os"],
      cpu: ["wrong-cpu"],
      optionalDependencies: { "@agent-director/linux-x64": "file:../broken" },
    });
    try {
      const result = runGuard(tmpDir, { PREPUBLISH_GUARD_MODE: "subpackage" });
      expect(result.exitCode).toBe(0);
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });
});

// ---------------------------------------------------------------------------
// (d) Unknown PREPUBLISH_GUARD_MODE
// ---------------------------------------------------------------------------

describe("publish-guard — bogus PREPUBLISH_GUARD_MODE", () => {
  test("exits 1 with 'unknown PREPUBLISH_GUARD_MODE=<value>' in stderr", () => {
    const tmpDir = makeTmpPkg({ name: "@real-scope/foo", version: "0.1.0" });
    try {
      const result = runGuard(tmpDir, { PREPUBLISH_GUARD_MODE: "bogusmode" });
      expect(result.exitCode).toBe(1);
      expect(result.stderr.toString()).toContain("unknown PREPUBLISH_GUARD_MODE=bogusmode");
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });
});

// ---------------------------------------------------------------------------
// (g) Default mode (unset PREPUBLISH_GUARD_MODE) — four-guard composite active
// ---------------------------------------------------------------------------

describe("publish-guard — default mode (composite)", () => {
  test("placeholder name → exit 1 (Guard 0 fires)", () => {
    const tmpDir = makeTmpPkg({ name: "@CHANGEME-H3/foo" });
    try {
      const result = runGuard(tmpDir); // PREPUBLISH_GUARD_MODE deliberately absent
      expect(result.exitCode).toBe(1);
      const stderr = result.stderr.toString();
      expect(stderr).toContain("CHANGEME-H3");
      expect(stderr).toContain("release-blockers.md");
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });

  test("real name + valid version, no SKILL.md → exit 1 (Guard 1 fires; composite is active)", () => {
    // Guard 0 passes (real name), Guard 1 fires because temp dir has no
    // adjacent SKILL.md — proving the four-guard composite runs in default mode.
    const tmpDir = makeTmpPkg({ name: "@real-scope/foo", version: "0.1.0" });
    try {
      const result = runGuard(tmpDir);
      expect(result.exitCode).toBe(1);
      expect(result.stderr.toString()).toContain("SKILL.md");
    } finally {
      cleanTmpPkg(tmpDir);
    }
  });
});

// ---------------------------------------------------------------------------
// (e) Wiring assertions
// ---------------------------------------------------------------------------

describe("publish-guard — wiring assertions", () => {
  const packageFiles = [
    resolve(pkgDir, "package.json"),
    resolve(pkgDir, "platforms", "linux-x64", "package.json"),
    resolve(pkgDir, "platforms", "darwin-arm64", "package.json"),
  ];

  // All three packages: prepublishOnly must invoke prepublish-guards.ts
  for (const pkgFile of packageFiles) {
    const label = pkgFile.replace(pkgDir + "/", "");
    test(`${label}: scripts.prepublishOnly invokes prepublish-guards.ts`, () => {
      const pkg = JSON.parse(readFileSync(pkgFile, "utf8")) as {
        scripts?: Record<string, string>;
      };
      const prepublishOnly = pkg.scripts?.prepublishOnly ?? "";
      expect(prepublishOnly, `${label} missing scripts.prepublishOnly`).toBeTruthy();
      expect(
        prepublishOnly,
        `${label}: prepublishOnly must invoke prepublish-guards.ts (got: ${prepublishOnly})`
      ).toContain("prepublish-guards.ts");
    });
  }

  // Platform sub-packages only: must also carry PREPUBLISH_GUARD_MODE=subpackage
  const platformFiles = [
    resolve(pkgDir, "platforms", "linux-x64", "package.json"),
    resolve(pkgDir, "platforms", "darwin-arm64", "package.json"),
  ];

  for (const pkgFile of platformFiles) {
    const label = pkgFile.replace(pkgDir + "/", "");
    test(`${label}: scripts.prepublishOnly includes PREPUBLISH_GUARD_MODE=subpackage`, () => {
      const pkg = JSON.parse(readFileSync(pkgFile, "utf8")) as {
        scripts?: Record<string, string>;
      };
      const prepublishOnly = pkg.scripts?.prepublishOnly ?? "";
      expect(
        prepublishOnly,
        `${label}: prepublishOnly must set PREPUBLISH_GUARD_MODE=subpackage (got: ${prepublishOnly})`
      ).toContain("PREPUBLISH_GUARD_MODE=subpackage");
    });
  }

  // All three packages: name must not carry the H3 placeholder
  for (const pkgFile of packageFiles) {
    const label = pkgFile.replace(pkgDir + "/", "");
    test(`${label}: name no longer contains the CHANGEME-H3 placeholder`, () => {
      const pkg = JSON.parse(readFileSync(pkgFile, "utf8")) as { name?: string };
      expect(typeof pkg.name).toBe("string");
      expect(
        (pkg.name ?? "").includes("CHANGEME-H3"),
        `${label} name still contains CHANGEME-H3 (got: ${pkg.name})`
      ).toBe(false);
    });
  }
});

// ---------------------------------------------------------------------------
// (f) Deletion invariant
// ---------------------------------------------------------------------------

describe("publish-guard — deletion invariant", () => {
  test("scripts/check-not-placeholder.ts is deleted from the repo", () => {
    const deletedPath = resolve(
      repoRoot,
      "pkg",
      "ts-bun-client",
      "scripts",
      "check-not-placeholder.ts"
    );
    expect(existsSync(deletedPath)).toBe(false);
  });
});
