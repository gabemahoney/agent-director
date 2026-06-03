/**
 * packaging.test.ts — verify the published tarball composition matches
 * the SR-6 surface (b.ue3 / Epic 4).
 *
 * After the b.ue3 cutover:
 *   - No `optionalDependencies` block.
 *   - No `bin` field.
 *   - No lifecycle scripts (11 forbidden hook names).
 *   - No platforms/** in the tarball.
 *   - No skills/install-agent-director/** in the tarball.
 *   - No bin/** in the tarball.
 *   - No *.test.* in the tarball.
 *   - exports."." unchanged: import → ./dist/index.js, types → ./dist/index.d.ts.
 *   - exports["./dist/version-floor.json"] is the SR-6.6 amendment site.
 */

import { test, expect, describe } from "bun:test";
import { spawnSync } from "node:child_process";
import { resolve, dirname } from "node:path";
import { fileURLToPath } from "node:url";

const pkgDir = resolve(dirname(fileURLToPath(import.meta.url)), "..");

interface PackEntry {
  files?: Array<{ path: string }>;
}

function getPackFiles(dir: string): string[] {
  const result = spawnSync(
    "npm",
    ["pack", "--dry-run", "--json"],
    { cwd: dir, encoding: "utf8" },
  );
  if (result.status !== 0) {
    throw new Error(
      `packaging.test.ts: npm pack --dry-run failed in ${dir}:\n${result.stderr}`,
    );
  }
  const parsed = JSON.parse(result.stdout) as PackEntry[];
  if (parsed.length === 0 || !parsed[0]?.files) {
    throw new Error(`packaging.test.ts: empty pack output for ${dir}`);
  }
  return parsed[0].files!.map((f) => f.path);
}

const FORBIDDEN_LIFECYCLE_HOOKS = [
  "preinstall",
  "install",
  "postinstall",
  "prepare",
  "prepack",
  "postpack",
  "prepublish",
  "prepublishOnly",
  "postpublish",
  "preprepare",
  "postprepare",
];

describe("packaging — umbrella tarball composition (SR-6.1 / SR-6.3 / SR-6.4 / SR-6.7)", () => {
  // Read the live package.json — these assertions are about the source-of-truth,
  // which is what `npm pack` ships verbatim.
  const pkg = JSON.parse(
    require("node:fs").readFileSync(resolve(pkgDir, "package.json"), "utf8"),
  ) as Record<string, unknown>;

  test("no optionalDependencies field (SR-6.4)", () => {
    expect(pkg.optionalDependencies).toBeUndefined();
  });

  test("no bin field (SR-6.7)", () => {
    expect(pkg.bin).toBeUndefined();
  });

  for (const hook of FORBIDDEN_LIFECYCLE_HOOKS) {
    test(`no lifecycle hook "${hook}" in scripts (SR-6.3)`, () => {
      const scripts = (pkg.scripts ?? {}) as Record<string, string>;
      expect(scripts[hook]).toBeUndefined();
    });
  }

  test('exports["."] points at the bundle (SR-4.0)', () => {
    const exp = (pkg.exports as Record<string, unknown>)["."] as Record<string, string>;
    expect(exp.import).toBe("./dist/index.js");
    expect(exp.types).toBe("./dist/index.d.ts");
  });

  test('exports["./dist/version-floor.json"] is published (SR-6.6)', () => {
    const exp = pkg.exports as Record<string, unknown>;
    expect(exp["./dist/version-floor.json"]).toBe("./dist/version-floor.json");
  });
});

describe("packaging — tarball file list (SR-6.1 / SR-6.2)", () => {
  const files = getPackFiles(pkgDir);

  test("no platforms/** in tarball (SR-6.5)", () => {
    expect(files.some((f) => f.startsWith("platforms/"))).toBe(false);
  });

  test("no skills/install-agent-director/** in tarball (SR-6.2)", () => {
    expect(files.some((f) => f.startsWith("skills/"))).toBe(false);
  });

  test("no scripts/postinstall.ts in tarball (SR-6.3)", () => {
    expect(files.some((f) => f === "scripts/postinstall.ts")).toBe(false);
  });

  test("no bin/** in tarball (SR-6.7)", () => {
    expect(files.some((f) => f.startsWith("bin/"))).toBe(false);
  });

  test("no .test. files in tarball", () => {
    expect(files.some((f) => f.includes(".test."))).toBe(false);
  });

  test("no native shared libraries (.so / .dylib) in tarball", () => {
    expect(files.some((f) => f.endsWith(".so") || f.endsWith(".dylib"))).toBe(false);
  });

  test("includes dist/index.js (SR-6.1)", () => {
    expect(files).toContain("dist/index.js");
  });

  test("includes dist/index.d.ts (SR-6.1)", () => {
    expect(files).toContain("dist/index.d.ts");
  });

  test("includes dist/version-floor.json (SR-6.1)", () => {
    expect(files).toContain("dist/version-floor.json");
  });

  test("includes README.md (SR-6.1)", () => {
    expect(files).toContain("README.md");
  });

  test("includes package.json (npm-automatic)", () => {
    expect(files).toContain("package.json");
  });
});
