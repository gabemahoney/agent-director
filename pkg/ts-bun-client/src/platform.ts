/**
 * platform — native library resolver for agent-director.
 *
 * Implements the five-step resolution sequence:
 *   1. Check Bun.version against MIN_BUN_VERSION → ErrBunVersionTooOld
 *   2. Map process.platform + process.arch to an npm sub-package name →
 *      ErrUnsupportedPlatform for unknown tuples
 *   3. Locate the sub-package on disk via import.meta.resolve →
 *      ErrPlatformPackageMissing when not installed
 *   4. Compute binary path inside the package and verify existence →
 *      ErrPlatformPackageMissing when binary absent (e.g. CI host mismatch)
 *   5. dlopen with the full binding spec → return { lib: symbols, libPath }
 *
 * resolveNativePath() exports just the path (used by bootstrapFfi.ts which
 * keeps its own minimal dlopen).
 *
 * loadNative() / _loadNativeInternal() dlopen the full binding spec and return
 * the symbols object together with the resolved path.
 *
 * _loadNativeInternal() accepts a PlatformLoadOpts bag for test injection
 * (override platform, arch, bunVersion without monkey-patching globals).
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { existsSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join, dirname } from "node:path";
import { dlopen } from "bun:ffi";
import {
  ErrUnsupportedPlatform,
  ErrPlatformPackageMissing,
  ErrBunVersionTooOld,
} from "./errors.js";
import { buildBindingSpec } from "./internal/bindingSpec.js";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Minimum Bun runtime version required by this package (from T1). */
export const MIN_BUN_VERSION = "1.0.21";

/**
 * Supported platform/arch tuples → npm sub-package names.
 * v1 scope is {linux-x64, darwin-arm64}; linux-arm64 is deferred to v2,
 * darwin-x64 was dropped 2026-05-24 (no Intel Mac users to serve).
 */
const SUPPORTED_TUPLES = new Map<string, string>([
  ["linux-x64", "@agent-director/linux-x64"],
  ["darwin-arm64", "@agent-director/darwin-arm64"],
]);

// ---------------------------------------------------------------------------
// Semver comparison (3-part only — Bun versions are always X.Y.Z)
// ---------------------------------------------------------------------------

function compareSemver(a: string, b: string): number {
  const parse = (s: string) => s.split(".").map((p) => parseInt(p, 10) || 0);
  const pa = parse(a);
  const pb = parse(b);
  for (let i = 0; i < 3; i++) {
    const diff = (pa[i] ?? 0) - (pb[i] ?? 0);
    if (diff !== 0) return diff;
  }
  return 0;
}

// ---------------------------------------------------------------------------
// DI options (override platform / arch / Bun.version in tests)
// ---------------------------------------------------------------------------

/**
 * PlatformLoadOpts — dependency-injection bag for loadNative / resolveNativePath.
 *
 * All fields are optional; missing fields fall back to the real process
 * globals. Pass these in unit tests to exercise error branches without
 * monkey-patching process.platform or Bun.version.
 */
export interface PlatformLoadOpts {
  /** Override process.platform (e.g. "linux", "darwin"). */
  platform?: string;
  /** Override process.arch (e.g. "x64", "arm64"). */
  arch?: string;
  /** Override Bun.version (e.g. "1.0.0" to trigger ErrBunVersionTooOld). */
  bunVersion?: string;
}

// ---------------------------------------------------------------------------
// resolveNativePath — step 1-4 only (no dlopen)
// ---------------------------------------------------------------------------

/**
 * resolveNativePath performs the platform check and sub-package lookup,
 * returning the absolute path to the native shared library.
 *
 * Throws one of:
 *   ErrBunVersionTooOld      — Bun version < MIN_BUN_VERSION
 *   ErrUnsupportedPlatform   — platform/arch tuple not in SUPPORTED_TUPLES
 *   ErrPlatformPackageMissing — sub-package not installed or binary absent
 */
export function resolveNativePath(opts: PlatformLoadOpts = {}): string {
  const platform = opts.platform ?? process.platform;
  const arch = opts.arch ?? process.arch;
  const bunVersion = opts.bunVersion ?? Bun.version;

  // Step 1: Bun version check.
  if (compareSemver(bunVersion, MIN_BUN_VERSION) < 0) {
    throw new ErrBunVersionTooOld(bunVersion, MIN_BUN_VERSION);
  }

  // Step 2: Tuple lookup.
  const tuple = `${platform}-${arch}`;
  const subpkgName = SUPPORTED_TUPLES.get(tuple);
  if (!subpkgName) {
    throw new ErrUnsupportedPlatform(tuple);
  }

  // Step 3: Locate sub-package via Bun's module resolver.
  // import.meta.resolve returns a file:// URL relative to this source file.
  let pkgDir: string;
  try {
    // Resolve the package.json inside the sub-package — it is always present
    // and avoids the need for a dummy index.js entry-point.
    const resolved = import.meta.resolve(`${subpkgName}/package.json`);
    pkgDir = dirname(fileURLToPath(resolved));
  } catch {
    throw new ErrPlatformPackageMissing(subpkgName);
  }

  // Step 4: Compute and verify binary path.
  const binaryExt = platform === "darwin" ? "dylib" : "so";
  const binaryName = `libagent_director.${binaryExt}`;
  const libPath = join(pkgDir, binaryName);

  if (!existsSync(libPath)) {
    throw new ErrPlatformPackageMissing(
      subpkgName,
      `binary "${binaryName}" not found in package directory ${pkgDir}`
    );
  }

  return libPath;
}

// ---------------------------------------------------------------------------
// NativeLib type — the symbols object returned by dlopen
// ---------------------------------------------------------------------------

/** Symbols returned by dlopen with the full binding spec. */
export type NativeLib = Record<string, unknown>;

// ---------------------------------------------------------------------------
// loadNative — steps 1-5 (resolve + dlopen)
// ---------------------------------------------------------------------------

/**
 * _loadNativeInternal is the testable core of loadNative().
 *
 * Accepts PlatformLoadOpts for dependency injection of platform / arch /
 * Bun.version so tests can exercise every error branch without live binaries.
 *
 * @returns { lib, libPath } — lib is the dlopen symbols object; libPath is the
 *   absolute path to the .so/.dylib that was loaded (useful for logging).
 */
export function _loadNativeInternal(
  opts: PlatformLoadOpts = {}
): { lib: NativeLib; libPath: string } {
  const libPath = resolveNativePath(opts);
  const bindingSpec = buildBindingSpec();
  // dlopen returns Library<T>; we access .symbols as Record<string, unknown>
  // since callers already cast through unknown in getSymbol().
  const loaded = dlopen(libPath, bindingSpec);
  return { lib: loaded.symbols as unknown as NativeLib, libPath };
}

/**
 * loadNative is the public entry point called by worker.ts at startup.
 *
 * Uses the real process.platform, process.arch, and Bun.version.
 */
export function loadNative(): { lib: NativeLib; libPath: string } {
  return _loadNativeInternal();
}
