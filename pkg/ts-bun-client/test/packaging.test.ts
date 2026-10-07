/**
 * packaging.test.ts — the published tarball matches the SR-6 surface (b.ue3 /
 * Epic 4): package.json carries no optionalDependencies, bin or lifecycle
 * scripts, exports the bundle and version-floor.json, and `npm pack` ships
 * dist + README only.
 */

import { test, expect } from "bun:test";
import { spawnSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const pkgDir = resolve(import.meta.dir, "..");
const pkg = JSON.parse(readFileSync(resolve(pkgDir, "package.json"), "utf8")) as Record<string, any>;

test("package.json: name, no optionalDependencies (SR-6.4), no bin (SR-6.7), no lifecycle hooks (SR-6.3)", () => {
  expect(pkg.name).toBe("agent-director");
  expect(pkg.optionalDependencies).toBeUndefined();
  expect(pkg.bin).toBeUndefined();
  const hooks = [
    "preinstall", "install", "postinstall", "prepare", "prepack", "postpack",
    "prepublish", "prepublishOnly", "postpublish", "preprepare", "postprepare",
  ];
  expect(hooks.filter((h) => h in (pkg.scripts ?? {}))).toEqual([]);
});

test("package.json exports the bundle (SR-4.0) and dist/version-floor.json (SR-6.6)", () => {
  expect(pkg.exports["."]).toEqual({ import: "./dist/index.js", types: "./dist/index.d.ts" });
  expect(pkg.exports["./dist/version-floor.json"]).toBe("./dist/version-floor.json");
});

test("npm pack ships dist, version-floor.json, README and package.json, and nothing excluded (SR-6.1/6.2/6.5/6.7)", () => {
  const r = spawnSync("npm", ["pack", "--dry-run", "--json"], { cwd: pkgDir, encoding: "utf8" });
  expect(r.status, r.stderr).toBe(0);
  const files = (JSON.parse(r.stdout) as Array<{ files: Array<{ path: string }> }>)[0]!.files.map((f) => f.path);
  for (const want of ["dist/index.js", "dist/index.d.ts", "dist/version-floor.json", "README.md", "package.json"]) {
    expect(files).toContain(want);
  }
  const excluded = files.filter((f) =>
    /^(platforms|skills|bin)\//.test(f) || f === "scripts/postinstall.ts" || f.includes(".test.") || /\.(so|dylib)$/.test(f)
  );
  expect(excluded).toEqual([]);
});
