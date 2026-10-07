/**
 * comparator.test.ts — SR-8.3: the internal strict SemVer 2.0 parser and
 * 3-way comparator (SR-2.1–2.4), with the dev-sentinel short-circuit.
 */

import { test, expect } from "bun:test";
import { parseVersion, compareVersions, DEV_SENTINEL } from "../src/internal/semver.js";

test.each([
  ["0.7.0", "0.7.0", 0],
  ["0.7.0", "0.7.1", -1],
  ["0.7.1", "0.7.0", 1],
  ["1.0.0", "0.99.99", 1],
  ["0.99.99", "1.0.0", -1],
  ["10.0.0", "9.99.99", 1],
  ["0.0.1", "0.0.0", 1],
  // Equal cores: no prerelease ranks above a prerelease; prereleases compare by ASCII.
  ["0.7.0-rc1", "0.7.0", -1],
  ["0.7.0", "0.7.0-rc1", 1],
  ["0.7.0-rc1", "0.7.0-rc2", -1],
  ["0.7.0-beta", "0.7.0-alpha", 1],
  ["0.7.0-rc1", "0.7.0-rc1", 0],
  // SR-2.3: the sentinel satisfies any floor; as a floor it would reject every release (SR-5.2).
  [DEV_SENTINEL, "99.99.99", 1],
  ["99.99.99", DEV_SENTINEL, -1],
  ["0.0.1", DEV_SENTINEL, -1],
  [DEV_SENTINEL, DEV_SENTINEL, 0],
] as const)("compareVersions(%p, %p) === %p", (a, b, want) => {
  expect(compareVersions(a, b)).toBe(want);
});

// SR-2.2: no canonicalization or repair.
test.each([
  ["v prefix", "v0.7.0"], ["build metadata", "0.7.0+abc123"], ["git describe", "v0.6.2-13-gcd6817c"],
  ["leading space", " 0.7.0"], ["trailing space", "0.7.0 "], ["trailing newline", "0.7.0\n"],
  ["leading newline", "\n0.7.0"], ["trailing NBSP", "0.7.0\u00a0"], ["empty", ""], ["two parts", "0.7"],
  ["four parts", "0.7.0.0"], ["non-numeric", "a.b.c"], ["bad prerelease char", "0.7.0-rc!"],
])("parseVersion rejects %s %p, and compareVersions throws on it", (_label, input) => {
  expect(parseVersion(input).ok).toBe(false);
  expect(() => compareVersions(input, "0.7.0")).toThrow();
  expect(() => compareVersions("0.7.0", input)).toThrow();
});

test("only the byte-exact '0.0.0-dev' parses as the sentinel", () => {
  expect(DEV_SENTINEL).toBe("0.0.0-dev");
  expect(parseVersion("0.0.0-dev")).toEqual({ ok: true, value: { kind: "sentinel" } });
  expect(parseVersion("0.0.0-DEV")).toMatchObject({ ok: true, value: { kind: "real", prerelease: "DEV" } });
});
