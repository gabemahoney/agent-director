/**
 * platformResolve.ts — Bun runtime-version guard.
 *
 * The vendored-binary resolution logic that used to live here is gone as of
 * b.ue3 (Stream A).  Discovery is now run by src/internal/discovery.ts
 * against the system install (HOME → PATH).  The only piece this file still
 * owns is the Bun-runtime-version check, fired at the top of every
 * Client.create() and resolveSystemBinary() call before any filesystem stat
 * (SR-7.7).
 *
 * Internal — not re-exported from src/index.ts.
 */

import { ErrBunVersionTooOld } from "../errors.js";

/** Minimum Bun runtime version required by this package. */
export const MIN_BUN_VERSION = "1.0.21";

/**
 * compareSemver — 3-part numeric compare.  Bun versions are always X.Y.Z;
 * the library's strict-SemVer parser handles the general case for AD's CLI
 * binary.  This helper is intentionally lenient for forward compat.
 */
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

/**
 * checkBunVersion throws ErrBunVersionTooOld when the running Bun runtime is
 * below MIN_BUN_VERSION.  Called once per Client.create() / resolveSystemBinary()
 * at the top of the pipeline (SR-7.7).
 */
export function checkBunVersion(): void {
  if (compareSemver(Bun.version, MIN_BUN_VERSION) < 0) {
    throw new ErrBunVersionTooOld(Bun.version, MIN_BUN_VERSION);
  }
}
