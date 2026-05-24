/**
 * errors-catalog-drift-failures.test.ts
 *
 * Unit tests for the compareSets() formatter in test/internal/driftCompare.ts.
 * These use synthetic input to verify that drift is reported correctly in both
 * directions and that allow-listed names never appear in the diff output.
 *
 * Does NOT touch the real catalog file or the real errors module.
 */
import { test, expect, describe } from "bun:test";
import { compareSets } from "./internal/driftCompare.js";

describe("driftCompare failure-message formatting", () => {
  test("catalog extra → reported under 'in catalog but not in TS'", () => {
    // catalog={A,B,C}, TS={A,B}, allow=[] → catalogOnly=[C]
    const result = compareSets(["A", "B"], ["A", "B", "C"], []);
    expect(result.catalogOnly).toEqual(["C"]);
    expect(result.tsOnly).toEqual([]);
    expect(result.formatted).toContain("in catalog but not in TS: [C]");
    expect(result.formatted).not.toContain("in TS but not in catalog");
  });

  test("TS extra → reported under 'in TS but not in catalog'", () => {
    // catalog={A,B}, TS={A,B,X}, allow=[] → tsOnly=[X]
    const result = compareSets(["A", "B", "X"], ["A", "B"], []);
    expect(result.catalogOnly).toEqual([]);
    expect(result.tsOnly).toEqual(["X"]);
    expect(result.formatted).toContain("in TS but not in catalog: [X]");
    expect(result.formatted).not.toContain("in catalog but not in TS");
  });

  test("allow-listed TS name is excluded from tsOnly", () => {
    // catalog={A}, TS={A,Y}, allow=[Y] → both sides empty
    const result = compareSets(["A", "Y"], ["A"], ["Y"]);
    expect(result.catalogOnly).toEqual([]);
    expect(result.tsOnly).toEqual([]);
    expect(result.formatted).toBe("");
  });

  test("allow-listed catalog name is excluded from catalogOnly", () => {
    // catalog={A,B,C}, TS={A,B}, allow=[C] → both sides empty
    const result = compareSets(["A", "B"], ["A", "B", "C"], ["C"]);
    expect(result.catalogOnly).toEqual([]);
    expect(result.tsOnly).toEqual([]);
    expect(result.formatted).toBe("");
  });

  test("both directions simultaneously", () => {
    // catalog={A,B,C}, TS={A,B,X}, allow=[] → catalogOnly=[C], tsOnly=[X]
    const result = compareSets(["A", "B", "X"], ["A", "B", "C"], []);
    expect(result.catalogOnly).toEqual(["C"]);
    expect(result.tsOnly).toEqual(["X"]);
    expect(result.formatted).toContain("in catalog but not in TS: [C]");
    expect(result.formatted).toContain("in TS but not in catalog: [X]");
  });

  test("header line present on any drift", () => {
    const result = compareSets(["A"], ["A", "Extra"], []);
    expect(result.formatted).toContain("errors-catalog-drift: TS subclass set != catalog");
  });

  test("no drift → formatted is empty string", () => {
    const result = compareSets(["A", "B"], ["A", "B"], []);
    expect(result.formatted).toBe("");
    expect(result.catalogOnly).toEqual([]);
    expect(result.tsOnly).toEqual([]);
  });
});
