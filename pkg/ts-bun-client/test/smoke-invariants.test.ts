/**
 * smoke-invariants.test.ts — the smoke suite's coverage contract (T7 9d):
 * (a) every verb in VERBS has test/smoke/<verb>.test.ts; (b) every smoke file
 * imports and calls withTempHome; (c) every smoke file outside the allow-list
 * has an error case (`instanceof Err` / `toBeInstanceOf(Err`).
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { VERBS } from "../src/internal/verbs.js";

// Verbs with no triggerable verb-level error: version and expire declare no
// ErrorNames; find-missing's ErrProbeUnsupported is never returned (SR-1.7).
const NO_ERROR_CASE_ALLOWLIST = new Set(["version", "expire", "find-missing"]);

const smokeDir = path.resolve(import.meta.dir, "smoke");
const read = (file: string) => fs.readFileSync(path.join(smokeDir, file), "utf-8");

test("(a) every verb has a smoke test file", () => {
  expect(VERBS.filter((verb) => !fs.existsSync(path.join(smokeDir, `${verb}.test.ts`)))).toEqual([]);
});

test("(b) every smoke file imports and calls withTempHome", () => {
  const offending = fs.readdirSync(smokeDir)
    .filter((f) => f.endsWith(".test.ts"))
    .filter((f) => !/import[^;]*withTempHome[^;]*from/.test(read(f)) || !read(f).includes("withTempHome("));
  expect(offending).toEqual([]);
});

test("(c) every non-allow-listed smoke file has an error-case test", () => {
  const offending = VERBS.filter((verb) => !NO_ERROR_CASE_ALLOWLIST.has(verb))
    .filter((verb) => fs.existsSync(path.join(smokeDir, `${verb}.test.ts`)))
    .filter((verb) => !/instanceof Err|toBeInstanceOf\(Err/.test(read(`${verb}.test.ts`)));
  expect(offending).toEqual([]);
});
