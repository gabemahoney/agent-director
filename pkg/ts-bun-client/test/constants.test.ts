/**
 * constants.test.ts — SR-8.5 coverage for MIN_BINARY_VERSION and
 * DEV_SENTINEL_VERSION public-surface constants (SR-4.5).
 *
 * Runtime assertions: shape, parse-validity, sentinel rejection.
 * Compile-time assertions: TS string-literal narrowing for the sentinel.
 */

import { test, expect, describe } from "bun:test";
import { MIN_BINARY_VERSION, DEV_SENTINEL_VERSION } from "../src/index.js";
import { parseVersion } from "../src/internal/semver.js";

describe("MIN_BINARY_VERSION", () => {
  test("type is string", () => {
    expect(typeof MIN_BINARY_VERSION).toBe("string");
  });
  test("passes SR-2.2 strict SemVer 2.0 parse", () => {
    expect(parseVersion(MIN_BINARY_VERSION).ok).toBe(true);
  });
  test("is not the dev sentinel (SR-5.2)", () => {
    expect(MIN_BINARY_VERSION).not.toBe("0.0.0-dev");
  });
});

describe("DEV_SENTINEL_VERSION", () => {
  test("byte-exact '0.0.0-dev'", () => {
    expect(DEV_SENTINEL_VERSION).toBe("0.0.0-dev");
    expect(Buffer.compare(
      Buffer.from(DEV_SENTINEL_VERSION, "utf8"),
      Buffer.from("0.0.0-dev", "utf8"),
    )).toBe(0);
  });
  test("TS string-literal type narrows to '0.0.0-dev' (compile-time)", () => {
    // If DEV_SENTINEL_VERSION were widened to plain `string`, this
    // assignment would fail at tsc --noEmit time. The bun runtime is
    // type-erased, so this test passes at runtime regardless; the load-
    // bearing assertion is the typecheck script.
    const x: "0.0.0-dev" = DEV_SENTINEL_VERSION;
    expect(x).toBe("0.0.0-dev");
  });
});
