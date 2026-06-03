/**
 * comparator.test.ts — SR-8.3 coverage for the internal SemVer 2.0 parser +
 * 3-way comparator + dev-sentinel short-circuit (SR-2.1 / SR-2.2 / SR-2.3 /
 * SR-2.4).
 *
 * The comparator is internal-only (SR-2.4); tests reach it via internal-module
 * imports under src/internal/, not via the public surface.
 */

import { test, expect, describe } from "bun:test";
import {
  parseVersion,
  compareVersions,
  compareParsed,
  DEV_SENTINEL,
} from "../src/internal/semver.js";

// ---------------------------------------------------------------------------
// SR-8.3: Real X.Y.Z vs real X.Y.Z (table-driven)
// ---------------------------------------------------------------------------

describe("compareVersions: real vs real", () => {
  const cases: Array<[string, string, -1 | 0 | 1]> = [
    ["0.7.0", "0.7.0", 0],
    ["0.7.0", "0.7.1", -1],
    ["0.7.1", "0.7.0", 1],
    ["1.0.0", "0.99.99", 1],
    ["0.99.99", "1.0.0", -1],
    ["10.0.0", "9.99.99", 1],
    ["0.0.1", "0.0.0", 1],
  ];
  for (const [a, b, want] of cases) {
    test(`compareVersions(${a}, ${b}) === ${want}`, () => {
      expect(compareVersions(a, b)).toBe(want);
    });
  }
});

// ---------------------------------------------------------------------------
// SR-8.3: Prerelease ordering
// ---------------------------------------------------------------------------

describe("compareVersions: prerelease ordering", () => {
  test("0.7.0-rc1 < 0.7.0 (no-prerelease > has-prerelease)", () => {
    expect(compareVersions("0.7.0-rc1", "0.7.0")).toBe(-1);
  });
  test("0.7.0 > 0.7.0-rc1", () => {
    expect(compareVersions("0.7.0", "0.7.0-rc1")).toBe(1);
  });
  test("0.7.0-rc1 < 0.7.0-rc2 (ASCII compare)", () => {
    expect(compareVersions("0.7.0-rc1", "0.7.0-rc2")).toBe(-1);
  });
  test("0.7.0-alpha < 0.7.0-beta (ASCII compare)", () => {
    expect(compareVersions("0.7.0-alpha", "0.7.0-beta")).toBe(-1);
  });
  test("equal prereleases compare 0", () => {
    expect(compareVersions("0.7.0-rc1", "0.7.0-rc1")).toBe(0);
  });
});

// ---------------------------------------------------------------------------
// SR-8.3: Sentinel vs real and sentinel vs sentinel
// ---------------------------------------------------------------------------

describe("compareVersions: dev sentinel", () => {
  test("compareVersions(DEV_SENTINEL, '0.7.0') >= 0", () => {
    expect(compareVersions(DEV_SENTINEL, "0.7.0")).toBeGreaterThanOrEqual(0);
  });
  test("compareVersions('0.7.0', DEV_SENTINEL) <= 0", () => {
    expect(compareVersions("0.7.0", DEV_SENTINEL)).toBeLessThanOrEqual(0);
  });
  test("compareVersions(DEV_SENTINEL, DEV_SENTINEL) === 0", () => {
    expect(compareVersions(DEV_SENTINEL, DEV_SENTINEL)).toBe(0);
  });
  test("sentinel satisfies arbitrary high floor", () => {
    // SR-2.3 step 1: dev-stamped binary is never classified as too old.
    expect(compareVersions(DEV_SENTINEL, "99.99.99")).toBeGreaterThanOrEqual(0);
  });
});

// ---------------------------------------------------------------------------
// SR-8.3: Real-vs-sentinel-floor reject-everything demonstration.
// If the floor were the sentinel, every real release would be "too old" —
// motivating the SR-5.2 prohibition on shipping the sentinel as the floor.
// ---------------------------------------------------------------------------

describe("real vs sentinel-as-floor: motivates SR-5.2 prohibition", () => {
  const reals = ["0.0.1", "0.7.0", "1.0.0", "99.99.99"];
  for (const real of reals) {
    test(`compareVersions(${real}, DEV_SENTINEL) < 0 — real is "too old" when floor is sentinel`, () => {
      expect(compareVersions(real, DEV_SENTINEL)).toBeLessThan(0);
    });
  }
});

// ---------------------------------------------------------------------------
// SR-8.3: Unparseable rejection (table-driven). Every "fail" example from
// SR-2.2 must be rejected as unparseable; no canonicalization or repair.
// ---------------------------------------------------------------------------

describe("parseVersion: SR-2.2 strict rejection", () => {
  const failCases: Array<[string, string]> = [
    ["leading v: v0.7.0", "v0.7.0"],
    ["build metadata: 0.7.0+abc123", "0.7.0+abc123"],
    ["git-describe shape: v0.6.2-13-gcd6817c", "v0.6.2-13-gcd6817c"],
    ["leading whitespace", " 0.7.0"],
    ["trailing whitespace", "0.7.0 "],
    ["trailing newline", "0.7.0\n"],
    ["leading newline", "\n0.7.0"],
    // Non-ASCII byte (NBSP appended).
    ["non-ASCII NBSP appended", "0.7.0 "],
    ["empty string", ""],
    ["two-segment core", "0.7"],
    ["four-segment core", "0.7.0.0"],
    ["non-numeric core", "a.b.c"],
    ["dotted prerelease with invalid char", "0.7.0-rc!"],
  ];
  for (const [label, input] of failCases) {
    test(`rejects ${label}`, () => {
      expect(parseVersion(input).ok).toBe(false);
    });
  }
  test("compareVersions throws on unparseable", () => {
    expect(() => compareVersions("v0.7.0", "0.7.0")).toThrow();
    expect(() => compareVersions("0.7.0", "0.7.0+abc")).toThrow();
  });
});

// ---------------------------------------------------------------------------
// SR-8.3: Byte-exact sentinel roundtrip (parser-direct variant; the
// resolveSystemBinary variant lands in Epic 3).
// ---------------------------------------------------------------------------

describe("byte-exact sentinel roundtrip", () => {
  test("DEV_SENTINEL constant is byte-exact '0.0.0-dev'", () => {
    expect(DEV_SENTINEL).toBe("0.0.0-dev");
    const expected = Buffer.from("0.0.0-dev", "utf8");
    expect(Buffer.compare(Buffer.from(DEV_SENTINEL, "utf8"), expected)).toBe(0);
  });
  test("parseVersion('0.0.0-dev') returns a sentinel result", () => {
    const r = parseVersion("0.0.0-dev");
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.value.kind).toBe("sentinel");
    }
  });
  test("aliasing does not trigger short-circuit: 0.0.0-DEV parses as a non-sentinel real version", () => {
    const r = parseVersion("0.0.0-DEV");
    expect(r.ok).toBe(true);
    if (r.ok) {
      expect(r.value.kind).toBe("real");
    }
  });
});

// ---------------------------------------------------------------------------
// SR-2.3: compareParsed direct shape checks (covers the typed compare path
// used by the discovery pipeline).
// ---------------------------------------------------------------------------

describe("compareParsed: direct", () => {
  test("two sentinel values compare equal", () => {
    const r = parseVersion("0.0.0-dev");
    expect(r.ok && compareParsed(r.value, r.value)).toBe(0);
  });
  test("sentinel beats real", () => {
    const a = parseVersion("0.0.0-dev");
    const b = parseVersion("1.2.3");
    if (a.ok && b.ok) {
      expect(compareParsed(a.value, b.value)).toBe(1);
      expect(compareParsed(b.value, a.value)).toBe(-1);
    }
  });
});
