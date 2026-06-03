/**
 * removed-exports.test.ts — SR-8.9 / SR-4.6.
 *
 * Asserts that the three vendored-binary-era error classes are gone from
 * the public surface (the SRD's hard cut):
 *   - ErrUnsupportedPlatform
 *   - ErrPlatformPackageMissing
 *   - ErrCliNotExecutable
 *
 * Both the runtime check (`*  as ad; expect(ad.X).toBeUndefined()`) and the
 * compile-time check (// @ts-expect-error against the named export) confirm
 * the deletions are effective.
 */

import { test, expect } from "bun:test";
import * as ad from "../src/index.js";

test("ErrUnsupportedPlatform is not exported (SR-4.6)", () => {
  expect((ad as Record<string, unknown>)["ErrUnsupportedPlatform"]).toBeUndefined();
});

test("ErrPlatformPackageMissing is not exported (SR-4.6)", () => {
  expect((ad as Record<string, unknown>)["ErrPlatformPackageMissing"]).toBeUndefined();
});

test("ErrCliNotExecutable is not exported (SR-4.6)", () => {
  expect((ad as Record<string, unknown>)["ErrCliNotExecutable"]).toBeUndefined();
});

test("@ts-expect-error blocks confirm compile-time absence (SR-4.6)", () => {
  // The following blocks compile only if tsc rejects the named imports.
  // The runtime test passes regardless (type-only checks).
  // @ts-expect-error — ErrUnsupportedPlatform was removed in b.ue3
  void (ad as typeof ad).ErrUnsupportedPlatform;
  // @ts-expect-error — ErrPlatformPackageMissing was removed in b.ue3
  void (ad as typeof ad).ErrPlatformPackageMissing;
  // @ts-expect-error — ErrCliNotExecutable was removed in b.ue3
  void (ad as typeof ad).ErrCliNotExecutable;
  expect(true).toBe(true);
});

test("new exports (b.ue3) are present", () => {
  expect(ad.ErrSystemInstallNotFound).toBeDefined();
  expect(ad.ErrSystemInstallTooOld).toBeDefined();
  expect(ad.ErrSystemInstallUnreachable).toBeDefined();
  expect(ad.resolveSystemBinary).toBeDefined();
  expect(typeof ad.MIN_BINARY_VERSION).toBe("string");
  expect(ad.DEV_SENTINEL_VERSION).toBe("0.0.0-dev");
});
