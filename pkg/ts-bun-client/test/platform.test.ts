/**
 * Unit tests for the platform resolver (src/platform.ts).
 *
 * Uses the exported `_loadNativeInternal` DI overload so tests can exercise
 * every error branch without live binaries or process-global monkey-patching.
 *
 * Cases:
 *   (a) Unsupported tuple (linux/arm64, darwin/x64) → ErrUnsupportedPlatform
 *   (b) Bun version below minimum       → ErrBunVersionTooOld
 *   (c) Sub-package installed but binary absent (darwin/arm64 on linux) → ErrPlatformPackageMissing
 *   (d) Real linux/amd64 happy path     → { lib, libPath } with ad_open callable
 */

import { test, expect, describe } from "bun:test";
import { _loadNativeInternal, resolveNativePath, MIN_BUN_VERSION } from "../src/platform.js";
import {
  ErrUnsupportedPlatform,
  ErrPlatformPackageMissing,
  ErrBunVersionTooOld,
  AgentDirectorError,
} from "../src/errors.js";

// ---------------------------------------------------------------------------
// (a) Unsupported platform/arch tuple
// ---------------------------------------------------------------------------

describe("platform resolver — unsupported tuple", () => {
  test("linux/arm64 → throws ErrUnsupportedPlatform", () => {
    let caught: unknown;
    try {
      resolveNativePath({ platform: "linux", arch: "arm64" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrUnsupportedPlatform);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect((caught as ErrUnsupportedPlatform).errName).toBe("ErrUnsupportedPlatform");
    expect((caught as ErrUnsupportedPlatform).errDescription).toContain("linux-arm64");
  });

  test("win32/x64 → throws ErrUnsupportedPlatform", () => {
    expect(() => resolveNativePath({ platform: "win32", arch: "x64" })).toThrow(
      ErrUnsupportedPlatform
    );
  });

  test("darwin/x64 (Intel Mac) → throws ErrUnsupportedPlatform", () => {
    // darwin/x64 was dropped from v1 on 2026-05-24; no Intel Mac users to
    // serve and the macos-13 GH-hosted runner billing multiplier (10x) was
    // not worth the spend.
    let caught: unknown;
    try {
      resolveNativePath({ platform: "darwin", arch: "x64" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrUnsupportedPlatform);
    expect((caught as ErrUnsupportedPlatform).errDescription).toContain("darwin-x64");
  });
});

// ---------------------------------------------------------------------------
// (b) Bun version below minimum
// ---------------------------------------------------------------------------

describe("platform resolver — Bun version too old", () => {
  test("bunVersion 1.0.0 (below 1.0.21) → throws ErrBunVersionTooOld", () => {
    let caught: unknown;
    try {
      resolveNativePath({ bunVersion: "1.0.0" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrBunVersionTooOld);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    const err = caught as ErrBunVersionTooOld;
    expect(err.errName).toBe("ErrBunVersionTooOld");
    expect(err.errDescription).toContain("1.0.0");
    expect(err.errDescription).toContain(MIN_BUN_VERSION);
  });

  test("bunVersion 0.9.9 → throws ErrBunVersionTooOld", () => {
    expect(() => resolveNativePath({ bunVersion: "0.9.9" })).toThrow(ErrBunVersionTooOld);
  });

  test("bunVersion equal to minimum → does NOT throw ErrBunVersionTooOld", () => {
    // Should proceed past the version check (may throw ErrPlatformPackageMissing
    // or ErrUnsupportedPlatform, but NOT ErrBunVersionTooOld).
    let caught: unknown;
    try {
      resolveNativePath({ bunVersion: MIN_BUN_VERSION });
    } catch (e) {
      caught = e;
    }
    expect(caught).not.toBeInstanceOf(ErrBunVersionTooOld);
  });
});

// ---------------------------------------------------------------------------
// (c) Sub-package installed but binary absent (darwin on linux CI)
// ---------------------------------------------------------------------------

describe("platform resolver — platform package missing or binary absent", () => {
  test("darwin/arm64 on linux → throws ErrPlatformPackageMissing", () => {
    // darwin-arm64 is installed (bun install with file: resolves both
    // optionalDependencies) but has no .dylib binary on a linux host →
    // resolveNativePath throws ErrPlatformPackageMissing.
    let caught: unknown;
    try {
      resolveNativePath({ platform: "darwin", arch: "arm64" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrPlatformPackageMissing);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    const err = caught as ErrPlatformPackageMissing;
    expect(err.errName).toBe("ErrPlatformPackageMissing");
    expect(err.errDescription).toContain("darwin-arm64");
  });
});

// ---------------------------------------------------------------------------
// (d) Real linux/amd64 happy path
// ---------------------------------------------------------------------------

describe("platform resolver — real linux/amd64 happy path", () => {
  test(
    "loadNative() returns { lib, libPath } with ad_open callable (linux/amd64)",
    () => {
      // Skip if not on linux/amd64 (CI is always linux/amd64 for this Epic).
      if (process.platform !== "linux" || process.arch !== "x64") {
        console.log(
          `platform.test.ts: skipping happy-path test (not linux/x64; got ${process.platform}/${process.arch})`
        );
        return;
      }

      let result: ReturnType<typeof _loadNativeInternal>;
      let threwErr: unknown;
      try {
        result = _loadNativeInternal();
      } catch (e) {
        threwErr = e;
      }

      if (threwErr !== undefined) {
        // If the binary is absent, this is a CI environment setup issue, not a
        // code bug. Provide a clear diagnostic and fail the test.
        throw new Error(
          `loadNative() threw unexpectedly on linux/x64: ${
            threwErr instanceof Error ? threwErr.message : String(threwErr)
          }\nEnsure platforms/linux-x64/libagent_director.so exists and bun install has been run.`
        );
      }

      expect(result!).toBeDefined();
      expect(typeof result!.libPath).toBe("string");
      expect(result!.libPath).toContain("libagent_director.so");
      expect(result!.lib).toBeDefined();

      // Verify ad_open is a callable symbol in the returned lib.
      const adOpen = result!.lib["ad_open"];
      expect(typeof adOpen).toBe("function");
    }
  );
});
