/**
 * version-bump.test.ts — scripts/version-bump.ts on a staged tree: the
 * umbrella-version site (the only live one) is stamped with or without
 * --target, a second run is a logged no-op, and an unknown selector is refused.
 */

import { test, expect, afterEach } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { removeStaged, runScript, stageScript, umbrellaPkg } from "./internal/stagedScript.js";

afterEach(removeStaged);

test.each([
  ["default (all sites)", []],
  ["--target umbrella-version", ["--target", "umbrella-version"]],
])("%s: stamps package.json; a second run writes nothing and logs skipped", (_label, extra) => {
  const s = stageScript("version-bump.ts", { "package.json": umbrellaPkg("0.1.0") });
  const pkg = join(s.pkgDir, "package.json");
  const args = ["--version", "9.9.9", ...extra];

  expect(runScript(s.script, args).exitCode).toBe(0);
  const stamped = readFileSync(pkg, "utf8");
  expect((JSON.parse(stamped) as { version: string }).version).toBe("9.9.9");

  const again = runScript(s.script, args);
  expect(again.exitCode).toBe(0);
  expect(readFileSync(pkg, "utf8")).toBe(stamped);
  expect(again.stdout + again.stderr).toMatch(/skipped/i);
});

test("unknown --target → non-zero exit naming the value and the valid selectors", () => {
  const s = stageScript("version-bump.ts", { "package.json": umbrellaPkg("0.1.0") });
  const r = runScript(s.script, ["--version", "9.9.9", "--target", "totally-invalid-xyz"]);
  expect(r.exitCode).not.toBe(0);
  expect(r.stdout + r.stderr).toContain("totally-invalid-xyz");
  expect(r.stdout + r.stderr).toContain("umbrella-version");
});
