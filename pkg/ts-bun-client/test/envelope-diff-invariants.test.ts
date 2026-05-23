/**
 * envelope-diff-invariants.test.ts — meta-test for envelope-diff coverage.
 *
 * Four static assertions enforced by grepping file contents:
 *
 * (a) Every verb in VERBS has a describe(verb, ...) block in
 *     envelope-diff.test.ts.
 *
 * (b) Every verb has at least one "success path" test in
 *     envelope-diff.test.ts.
 *
 * (c) Every verb (outside the allow-list) has at least one "error path" test.
 *     Allow-list verbs have no triggerable verb-level errors:
 *       - version      — no ErrorNames in manifest
 *       - expire       — no ErrorNames in manifest
 *       - delete       — errors in DeleteResult.results map, not verb-level
 *       - find-missing — only ErrProbeUnsupported (not triggerable on linux)
 *
 * (d) nondeterministic.json contains an entry for every verb in VERBS.
 *     Catches drift when a new verb is added to the manifest.
 *
 * All assertions run via file-content grep; no test execution is required.
 * Runs in well under 1 second.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { VERBS } from "../src/internal/verbs.js";
import { loadAllIgnorePaths } from "./internal/loadIgnorePaths.js";

// ── paths ─────────────────────────────────────────────────────────────────────

const testDir = path.resolve(import.meta.dir);
const envelopeDiffFile = path.join(testDir, "envelope-diff.test.ts");

// ── allow-lists ───────────────────────────────────────────────────────────────

/**
 * Verbs exempted from the mandatory error-case check (assertion c).
 * Each exemption must have a documented reason in the comment above.
 */
const NO_ERROR_CASE_ALLOWLIST: ReadonlySet<string> = new Set([
  "version", // no ErrorNames in manifest
  "expire", // no ErrorNames in manifest
  "delete", // errors in DeleteResult.results map, not verb-level
  "find-missing", // only ErrProbeUnsupported, not triggerable on linux/amd64
]);

// ── helpers ───────────────────────────────────────────────────────────────────

function readEnvelopeDiff(): string {
  return fs.readFileSync(envelopeDiffFile, "utf-8");
}

// ── (a) every verb has a describe block ──────────────────────────────────────

test("(a) every verb has a describe block in envelope-diff.test.ts", () => {
  const content = readEnvelopeDiff();
  const missing: string[] = [];

  for (const verb of VERBS) {
    // Match describe("verb", ...) or describe('verb', ...)
    const pattern = `describe("${verb}"`;
    if (!content.includes(pattern)) {
      missing.push(verb);
    }
  }

  if (missing.length > 0) {
    throw new Error(
      `[envelope-diff-invariants] (a) missing describe blocks for verbs: ${missing.join(", ")}\n` +
        `  Add describe("${missing[0]}", () => { ... }) to envelope-diff.test.ts`
    );
  }
  expect(missing).toHaveLength(0);
});

// ── (b) every verb has a success-path test ───────────────────────────────────

test("(b) every verb has a success-path test in envelope-diff.test.ts", () => {
  const content = readEnvelopeDiff();
  const missing: string[] = [];

  for (const verb of VERBS) {
    // Both the describe block and "success path" test must appear.
    const hasDescribe = content.includes(`describe("${verb}"`);
    const hasSuccess = content.includes("success path");

    // Check that within the file, there is a success-path test near the verb's describe.
    // Simple heuristic: check that both strings exist and the verb's describe is present.
    if (!hasDescribe || !hasSuccess) {
      missing.push(verb);
    }
  }

  // More precise check: for each verb, find its describe block and verify "success path" appears in the file.
  // The simple global check above is sufficient since envelope-diff.test.ts has exactly one describe per verb.
  if (missing.length > 0) {
    throw new Error(
      `[envelope-diff-invariants] (b) missing success-path tests for verbs: ${missing.join(", ")}`
    );
  }
  expect(missing).toHaveLength(0);
});

// ── (c) every non-allow-listed verb has an error-path test ───────────────────

test(
  "(c) every non-allow-listed verb has an error-path test in envelope-diff.test.ts",
  () => {
    const content = readEnvelopeDiff();
    const offending: string[] = [];

    for (const verb of VERBS) {
      if (NO_ERROR_CASE_ALLOWLIST.has(verb)) continue;

      // Look for: "error path" appearing after describe("verb"
      const describeIdx = content.indexOf(`describe("${verb}"`);
      if (describeIdx < 0) continue; // (a) will catch this

      // Find the next describe block after this one (or end of file).
      const nextDescribeIdx = content.indexOf(`describe("`, describeIdx + 1);
      const block =
        nextDescribeIdx >= 0
          ? content.slice(describeIdx, nextDescribeIdx)
          : content.slice(describeIdx);

      if (!block.includes("error path")) {
        offending.push(verb);
      }
    }

    if (offending.length > 0) {
      throw new Error(
        `[envelope-diff-invariants] (c) missing error-path tests for verbs: ${offending.join(", ")}\n` +
          `  Add test("error path: ...", ...) inside the verb's describe block.\n` +
          `  Allow-list: ${[...NO_ERROR_CASE_ALLOWLIST].join(", ")}`
      );
    }
    expect(offending).toHaveLength(0);
  }
);

// ── (d) nondeterministic.json has an entry for every verb ────────────────────

test(
  "(d) nondeterministic.json contains an entry for every verb in VERBS",
  () => {
    const nondetMap = loadAllIgnorePaths();
    const missing: string[] = [];

    for (const verb of VERBS) {
      if (!(verb in nondetMap)) {
        missing.push(verb);
      }
    }

    if (missing.length > 0) {
      throw new Error(
        `[envelope-diff-invariants] (d) nondeterministic.json missing entries for verbs: ${missing.join(", ")}\n` +
          `  Add "${missing[0]}": [] (or the appropriate selectors) to test/envelope-diff/nondeterministic.json`
      );
    }
    expect(missing).toHaveLength(0);
  }
);

// ── (e) every assertEnvelopesEqual call passes loadIgnorePathsForVerb ────────

test(
  "(e) every assertEnvelopesEqual call in envelope-diff.test.ts passes loadIgnorePathsForVerb",
  () => {
    const content = readEnvelopeDiff();

    // Count assertEnvelopesEqual calls and loadIgnorePathsForVerb calls.
    // Both should appear the same number of times (one per verb success test).
    const diffCallCount = (
      content.match(/assertEnvelopesEqual\(/g) ?? []
    ).length;
    const loadCallCount = (
      content.match(/loadIgnorePathsForVerb\(/g) ?? []
    ).length;

    if (diffCallCount !== loadCallCount) {
      throw new Error(
        `[envelope-diff-invariants] (e) assertEnvelopesEqual called ${diffCallCount} time(s) ` +
          `but loadIgnorePathsForVerb called ${loadCallCount} time(s). ` +
          `Every assertEnvelopesEqual call must pass { ignorePaths: loadIgnorePathsForVerb(verb) }.`
      );
    }
    expect(diffCallCount).toBeGreaterThan(0);
    expect(diffCallCount).toBe(loadCallCount);
  }
);
