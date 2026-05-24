/**
 * Smoke-invariants meta-test (T7 subtask 9d).
 *
 * Three static assertions enforced by grepping test file contents:
 *
 * (a) Every verb in src/internal/verbs.ts::VERBS has a smoke test file at
 *     test/smoke/<verb>.test.ts. Failure names the missing verb.
 *
 * (b) Every smoke test file imports withTempHome AND calls it (static grep).
 *     Failure names the offending file.
 *
 * (c) Every smoke test file has at least one error-case test that uses
 *     `instanceof Err` or `toBeInstanceOf(Err`. Failure names the file.
 *     Allow-list:
 *       - "version"      — no ErrorNames in manifest
 *       - "expire"       — no ErrorNames in manifest
 *       - "delete"       — no verb-level ErrorNames; errors in result map
 *       - "find-missing" — only ErrProbeUnsupported (untriggerable on linux)
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { VERBS } from "../src/internal/verbs.js";

// Allow-list for assertion (c) — verbs whose smoke files are exempted from
// the mandatory error-case grep because the verb has no triggerable verb-level
// errors per the manifest.
const NO_ERROR_CASE_ALLOWLIST: ReadonlySet<string> = new Set([
  "version",      // manifest ErrorNames: []
  "expire",       // manifest ErrorNames: []
  "delete",       // manifest ErrorNames: [] — errors in DeleteResult.results map
  "find-missing", // manifest ErrorNames: ["ErrProbeUnsupported"] (linux only, untriggerable)
]);

const smokeDir = path.resolve(import.meta.dir, "smoke");

// ---------------------------------------------------------------------------
// (a) Every verb has a test/smoke/<verb>.test.ts file.
// ---------------------------------------------------------------------------
test("(a) every verb has a smoke test file", () => {
  const missing: string[] = [];
  for (const verb of VERBS) {
    const file = path.join(smokeDir, `${verb}.test.ts`);
    if (!fs.existsSync(file)) {
      missing.push(verb);
    }
  }
  if (missing.length > 0) {
    throw new Error(
      `[smoke-invariants] (a) missing smoke test files for verbs: ${missing.join(", ")}\n` +
        `  Expected files at: test/smoke/<verb>.test.ts`
    );
  }
  expect(missing).toHaveLength(0);
});

// ---------------------------------------------------------------------------
// (b) Every smoke file imports withTempHome and calls it.
// ---------------------------------------------------------------------------
test("(b) every smoke file imports and calls withTempHome", () => {
  const offending: string[] = [];
  const smokeFiles = fs
    .readdirSync(smokeDir)
    .filter((f) => f.endsWith(".test.ts"));

  for (const filename of smokeFiles) {
    const filePath = path.join(smokeDir, filename);
    const content = fs.readFileSync(filePath, "utf-8");

    const hasImport =
      content.includes("withTempHome") && content.includes("from");
    const hasCall = content.includes("withTempHome(");

    if (!hasImport || !hasCall) {
      offending.push(filename);
    }
  }

  if (offending.length > 0) {
    throw new Error(
      `[smoke-invariants] (b) smoke files missing withTempHome import or call:\n` +
        offending.map((f) => `  - ${f}`).join("\n")
    );
  }
  expect(offending).toHaveLength(0);
});

// ---------------------------------------------------------------------------
// (c) Every smoke file (outside allow-list) has at least one error-case test.
// ---------------------------------------------------------------------------
test("(c) every non-allow-listed smoke file has an error-case test", () => {
  const offending: string[] = [];

  for (const verb of VERBS) {
    if (NO_ERROR_CASE_ALLOWLIST.has(verb)) continue;

    const filePath = path.join(smokeDir, `${verb}.test.ts`);
    if (!fs.existsSync(filePath)) continue; // (a) already caught this

    const content = fs.readFileSync(filePath, "utf-8");
    const hasErrorCase =
      content.includes("instanceof Err") ||
      content.includes("toBeInstanceOf(Err");

    if (!hasErrorCase) {
      offending.push(`${verb}.test.ts`);
    }
  }

  if (offending.length > 0) {
    throw new Error(
      `[smoke-invariants] (c) smoke files missing error-case test (instanceof Err / toBeInstanceOf(Err):\n` +
        offending.map((f) => `  - ${f}`).join("\n") +
        `\n  Allow-list: ${[...NO_ERROR_CASE_ALLOWLIST].join(", ")}`
    );
  }
  expect(offending).toHaveLength(0);
});
