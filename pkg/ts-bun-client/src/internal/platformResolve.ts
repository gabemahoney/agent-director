/**
 * platformResolve.ts — CLI binary resolver for the subprocess Client.
 *
 * Resolves the path to the bundled agent-director CLI binary at Client
 * construction time (one-shot; not per-call). Resolution steps:
 *
 *   1. Check Bun.version against MIN_BUN_VERSION → ErrBunVersionTooOld
 *   2. Map process.platform + process.arch to an npm sub-package name →
 *      ErrUnsupportedPlatform for unknown tuples
 *   3. Locate the CLI binary path:
 *      - In production: resolve the sub-package via import.meta.resolve on
 *        its package.json, then construct `<pkgDir>/bin/agent-director`.
 *      - In tests: a `_requireResolve` DI hook can short-circuit step 3 and
 *        return the binary path directly.
 *   4. Stat the resolved binary path → ErrPlatformPackageMissing when absent.
 *   5. Check execute bits against current process effective uid/gid →
 *      ErrCliNotExecutable when not executable.
 *
 * Implements SRD SR-2.1 (resolution at construction), SR-2.2
 * (ErrPlatformPackageMissing), SR-2.3 (ErrCliNotExecutable), SR-2.4
 * (one-shot; no per-call re-check).
 *
 * Internal — NOT re-exported from src/index.ts.
 */

import { statSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { join, dirname } from "node:path";
import {
  ErrUnsupportedPlatform,
  ErrPlatformPackageMissing,
  ErrBunVersionTooOld,
  ErrCliNotExecutable,
} from "../errors.js";

// Re-export the error classes expected by tests that import directly from
// this module instead of from src/errors.ts.
export { ErrPlatformPackageMissing, ErrCliNotExecutable } from "../errors.js";

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

/** Minimum Bun runtime version required by this package. */
export const MIN_BUN_VERSION = "1.0.21";

/**
 * Supported platform/arch tuples → npm sub-package names.
 * Must stay in sync with pkg/ts-bun-client/src/platform.ts::SUPPORTED_TUPLES.
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
// DI options (for testing)
// ---------------------------------------------------------------------------

/**
 * ResolveCliPathOpts provides dependency-injection hooks for testing without
 * requiring a real installed platform package.
 *
 * When `_requireResolve` is provided it is called with the full package
 * specifier and must return the absolute path to the CLI binary directly
 * (not a package.json path). Throwing from it simulates a missing package.
 */
export interface ResolveCliPathOpts {
  /** Override process.platform (e.g. "linux"). Falls back to the real value. */
  platform?: string;
  /** Override process.arch (e.g. "x64"). Falls back to the real value. */
  arch?: string;
  /**
   * Override the module-resolution step (step 3).
   *
   * When supplied, this function is called with the platform sub-package name
   * and must return the **absolute path to the CLI binary** directly.
   * Throwing from it signals that the package is not installed and will cause
   * resolveCliPath to throw ErrPlatformPackageMissing.
   *
   * In production, `import.meta.resolve` is used instead; the package.json is
   * resolved and then `<pkgDir>/bin/agent-director` is constructed from it.
   */
  _requireResolve?: (pkg: string) => string;
}

// ---------------------------------------------------------------------------
// resolveCliPath
// ---------------------------------------------------------------------------

/**
 * resolveCliPath resolves the absolute path to the bundled CLI binary for the
 * current platform. Throws typed errors on every failure path; never returns
 * a path that does not point at an executable file.
 *
 * The returned path is intended to be cached on the Client instance.
 *
 * @param opts Optional DI overrides for testing.
 *
 * Throws one of:
 *   ErrBunVersionTooOld        — Bun version < MIN_BUN_VERSION
 *   ErrUnsupportedPlatform     — platform/arch tuple not in SUPPORTED_TUPLES
 *   ErrPlatformPackageMissing  — sub-package not installed or binary absent
 *   ErrCliNotExecutable        — binary exists but lacks execute permission
 */
export function resolveCliPath(opts?: ResolveCliPathOpts): string {
  const platform = opts?.platform ?? process.platform;
  const arch = opts?.arch ?? process.arch;

  // Step 1: Bun version check.
  if (compareSemver(Bun.version, MIN_BUN_VERSION) < 0) {
    throw new ErrBunVersionTooOld(Bun.version, MIN_BUN_VERSION);
  }

  // Step 2: Platform/arch tuple lookup.
  const tuple = `${platform}-${arch}`;
  const subpkgName = SUPPORTED_TUPLES.get(tuple);
  if (subpkgName === undefined) {
    throw new ErrUnsupportedPlatform(tuple);
  }

  // Step 3: Resolve the binary path.
  //
  // Production path: resolve package.json → pkgDir → pkgDir/bin/agent-director.
  // Test path: _requireResolve returns the binary path directly.
  let binPath: string;

  if (opts?._requireResolve !== undefined) {
    // DI path: the hook returns the binary path directly.
    try {
      binPath = opts._requireResolve(subpkgName);
    } catch {
      throw new ErrPlatformPackageMissing(
        subpkgName,
        `platform package not installed (DI hook threw); run: npm install ${subpkgName}`
      );
    }
  } else {
    // Production path: resolve via package.json to get pkgDir.
    let pkgDir: string;
    try {
      // Resolve package.json (always present); the binary itself is not a JS
      // module and cannot be resolved directly via import.meta.resolve.
      const pkgJsonUrl = import.meta.resolve(`${subpkgName}/package.json`);
      pkgDir = dirname(fileURLToPath(pkgJsonUrl));
    } catch {
      throw new ErrPlatformPackageMissing(
        subpkgName,
        `platform package not installed; run: npm install ${subpkgName}`
      );
    }
    binPath = join(pkgDir, "bin", "agent-director");
  }

  // Step 4 & 5: Stat the binary and check execute permission.
  let stat: ReturnType<typeof statSync>;
  try {
    stat = statSync(binPath);
  } catch {
    throw new ErrPlatformPackageMissing(
      subpkgName,
      `binary not found at "${binPath}"`
    );
  }

  // Check execute permission against current process effective uid/gid.
  // Strategy: check S_IXUSR if uid matches, S_IXGRP if gid matches,
  // S_IXOTH as fallback. At least one must be set.
  const mode = stat.mode;
  const fileUid = stat.uid;
  const fileGid = stat.gid;
  const myUid = process.getuid?.() ?? 0;
  const myGid = process.getgid?.() ?? 0;

  const execByOwner = (mode & 0o100) !== 0;
  const execByGroup = (mode & 0o010) !== 0;
  const execByOther = (mode & 0o001) !== 0;

  const isExecutable =
    (fileUid === myUid && execByOwner) ||
    (fileGid === myGid && execByGroup) ||
    execByOther;

  if (!isExecutable) {
    throw new ErrCliNotExecutable(binPath);
  }

  return binPath;
}
