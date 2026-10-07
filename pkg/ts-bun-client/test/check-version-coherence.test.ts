/**
 * check-version-coherence.test.ts — scripts/check-version-coherence.ts on a
 * staged tree: site 3a (umbrella package.json::version), site-dist-no-inline
 * (SR-2.3, both scopes), the floor lockstep (SR-5.4) and, under --scope
 * publish, the tarball SHA-256 manifest (SR-1.3).
 */

import { test, expect, afterEach } from "bun:test";
import { createHash } from "node:crypto";
import { rmSync, writeFileSync } from "node:fs";
import { join, relative, resolve, sep } from "node:path";
import { removeStaged, runScript, stageScript, umbrellaPkg, type Staged } from "./internal/stagedScript.js";

const EXPECTED = "9.9.9";
const FLOOR_JSON = `{\n  "min_binary_version": "0.7.0"\n}\n`;
const CLEAN_DIST = `export const MIN_BINARY_VERSION = "0.7.0";\nexport const DEV_SENTINEL_VERSION = "0.0.0-dev";\n`;

afterEach(removeStaged);

/** A coherent tree at EXPECTED, with overrides; plus a one-tarball SHA manifest at the root. */
function stage(opts: { version?: string; dist?: string } = {}): Staged & { shasums: string } {
  const s = stageScript("check-version-coherence.ts", {
    "package.json": umbrellaPkg(opts.version ?? EXPECTED),
    "dist/index.js": opts.dist ?? CLEAN_DIST,
    "version-floor.json": FLOOR_JSON,
    "dist/version-floor.json": FLOOR_JSON,
  });
  const tgz = join(s.root, "dummy.tgz");
  writeFileSync(tgz, "tarball stub\n");
  const shasums = join(s.root, "tarball-shasums.txt");
  writeFileSync(shasums, `${createHash("sha256").update("tarball stub\n").digest("hex")}  ${tgz}\n`);
  return { ...s, shasums };
}

function check(s: Staged & { shasums: string }, scope: string, version = EXPECTED) {
  return runScript(s.script, ["--scope", scope, "--expected-version", version], { AGENT_DIRECTOR_RELEASE_SHASUMS: s.shasums });
}

test("staging tree lives outside the repo tree (b.9qj)", () => {
  expect(relative(resolve(import.meta.dir, "../../.."), stage().root).split(sep)[0]).toBe("..");
});

test.each(["verify", "publish"])("--scope %s on a coherent tree → exit 0, empty stderr", (scope) => {
  const r = check(stage(), scope);
  expect([r.exitCode, r.stderr]).toEqual([0, ""]);
});

test("site-3a: umbrella package.json at the wrong version → exit 1 naming the file, actual and expected", () => {
  const s = stage({ version: "0.0.1" });
  const r = check(s, "publish");
  expect(r.exitCode).not.toBe(0);
  for (const want of [join(s.pkgDir, "package.json"), "0.0.1", EXPECTED]) expect(r.stderr).toContain(want);
});

test.each([
  [["--scope", "foo", "--expected-version", EXPECTED], "foo"],
  [["--scope", "publish", "--expected-version", `v${EXPECTED}`], `v${EXPECTED}`],
  [["--scope", "publish"], "--expected-version"],
])("bad flags %p → exit 1, stderr names %s", (args, want) => {
  const r = runScript(stage().script, args);
  expect(r.exitCode).not.toBe(0);
  expect(r.stderr).toContain(want);
});

// SR-2.2: publish ⊇ verify, so the dist negative-grep fires under both scopes.
test.each([
  ["verify", 'const NPM_PACKAGE_VERSION = "1.2.3";', "NPM_PACKAGE_VERSION"],
  ["verify", 'const version = "0.0.0";', '"0.0.0"'],
  ["publish", 'const NPM_PACKAGE_VERSION = "1.2.3";', "NPM_PACKAGE_VERSION"],
  ["publish", 'const version = "0.0.0";', '"0.0.0"'],
])("site-dist-no-inline --scope %s: dist/index.js %s → exit 1 naming it", (scope, dist, want) => {
  const r = check(stage({ dist }), scope);
  expect(r.exitCode).not.toBe(0);
  expect(r.stderr).toContain(want);
});

test("site-dist-no-inline: dist/index.js absent → exit 1 naming the missing path", () => {
  const s = stage();
  rmSync(join(s.pkgDir, "dist", "index.js"));
  const r = check(s, "verify");
  expect(r.exitCode).not.toBe(0);
  expect(r.stderr).toContain(join(s.pkgDir, "dist", "index.js"));
});
