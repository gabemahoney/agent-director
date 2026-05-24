/**
 * Verb-binding coverage test.
 *
 * Asserts that the BINDING_SYMBOL_NAMES list exported from src/ffi.ts
 * (originally from src/internal/bindingSpec.ts) exactly matches the expected
 * set of symbols that the worker's dlopen binding object must declare:
 *
 *   ["ad_open", "ad_close", "ad_free_cstring", ...VERBS.map(v => "ad_" + v.replace(/-/g, "_"))]
 *
 * This test runs WITHOUT loading a native library — it only imports static
 * TypeScript constants.
 *
 * On mismatch, the failure message names missing and extra symbols.
 */

import { test, expect, describe } from "bun:test";
import { VERBS, kebabToUnderscore } from "../src/internal/verbs.js";
import { BINDING_SYMBOL_NAMES } from "../src/ffi.js";

describe("FFI binding coverage", () => {
  test("BINDING_SYMBOL_NAMES exactly matches expected symbol set", () => {
    const expected = new Set<string>([
      "ad_open",
      "ad_close",
      "ad_free_cstring",
      ...VERBS.map((v) => "ad_" + kebabToUnderscore(v)),
    ]);

    const actual = new Set<string>(BINDING_SYMBOL_NAMES);

    const missing = [...expected].filter((s) => !actual.has(s));
    const extra = [...actual].filter((s) => !expected.has(s));

    const lines: string[] = [];
    if (missing.length > 0) {
      lines.push(`Missing from BINDING_SYMBOL_NAMES:\n  ${missing.join("\n  ")}`);
    }
    if (extra.length > 0) {
      lines.push(`Extra (unexpected) in BINDING_SYMBOL_NAMES:\n  ${extra.join("\n  ")}`);
    }

    if (lines.length > 0) {
      throw new Error(
        "BINDING_SYMBOL_NAMES does not match expected set:\n\n" + lines.join("\n\n")
      );
    }

    expect(actual.size).toBe(expected.size);
  });

  test("BINDING_SYMBOL_NAMES contains all 15 callable verbs as ad_<underscore>", () => {
    const actual = new Set<string>(BINDING_SYMBOL_NAMES);

    for (const verb of VERBS) {
      const sym = "ad_" + kebabToUnderscore(verb);
      expect(actual.has(sym)).toBe(true);
    }
  });

  test("BINDING_SYMBOL_NAMES contains lifecycle symbols", () => {
    const actual = new Set<string>(BINDING_SYMBOL_NAMES);
    expect(actual.has("ad_open")).toBe(true);
    expect(actual.has("ad_close")).toBe(true);
    expect(actual.has("ad_free_cstring")).toBe(true);
  });

  test("BINDING_SYMBOL_NAMES total count is 15 verbs + 3 lifecycle = 18", () => {
    expect(BINDING_SYMBOL_NAMES.length).toBe(18);
  });
});
