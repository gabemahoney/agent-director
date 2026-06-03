/**
 * discovery.ts — system-install discovery pipeline (b.ue3 / SR-1).
 *
 * Implements the two-step lookup defined in SR-1.1:
 *   1. Standard install path: $HOME/.agent-director/bin/agent-director.
 *      Skipped (no candidate manufactured) when HOME is unset/empty/non-absolute.
 *   2. PATH lookup for "agent-director", first match.
 *
 * Each candidate is validated against SR-1.2 (regular-file + exec-bit) before
 * being returned.  Standard-install-path validation failures do NOT fall
 * through to PATH (SR-1.2).
 *
 * Internal — not re-exported from src/index.ts.
 */

import { statSync, realpathSync, accessSync, constants as fsConstants } from "node:fs";
import { resolve, isAbsolute, join, dirname } from "node:path";
import { delimiter } from "node:path";
import type { Stats } from "node:fs";
import {
  ErrSystemInstallNotFound,
  ErrSystemInstallUnreachable,
  type CheckedLocation,
} from "../errors.js";

const STANDARD_INSTALL_PATH_RELATIVE = ".agent-director/bin/agent-director";
const CLI_BINARY_NAME = "agent-director";

/** Result of a successful candidate validation. */
export interface DiscoveredCandidate {
  /** Canonicalized absolute path to the binary (symlinks resolved). */
  path: string;
  /** Which step produced this candidate. */
  kind: "standard-install-path" | "path-lookup";
}

/**
 * resolveStandardInstallPath returns the absolute candidate path or null when
 * HOME is unset, empty, or not an absolute path (SR-1.1 HOME-unset sub-clause).
 */
function resolveStandardInstallPath(homeEnv: string | undefined): string | null {
  if (homeEnv === undefined || homeEnv === "" || !isAbsolute(homeEnv)) {
    return null;
  }
  return resolve(homeEnv, STANDARD_INSTALL_PATH_RELATIVE);
}

/**
 * pathLookup walks the colon-separated PATH and returns the first directory
 * that contains an "agent-director" entry.  Returns null when PATH is
 * unset/empty or no match is found.
 */
function pathLookup(pathEnv: string | undefined): string | null {
  if (pathEnv === undefined || pathEnv === "") return null;
  for (const dir of pathEnv.split(delimiter)) {
    if (dir === "") continue;
    const candidate = join(dir, CLI_BINARY_NAME);
    try {
      const st = statSync(candidate);
      if (st.isFile() || st.isSymbolicLink()) {
        return candidate;
      }
    } catch {
      // not present; try next directory
    }
  }
  return null;
}

/**
 * isExecutableFor returns true when the calling process can execute the file
 * per SR-1.2's owner/group/other precedence rule.
 */
function isExecutableFor(st: Stats): boolean {
  const mode = st.mode;
  const fileUid = st.uid;
  const fileGid = st.gid;
  const myUid = process.getuid?.() ?? 0;
  const myGid = process.getgid?.() ?? 0;

  if (fileUid === myUid) return (mode & 0o100) !== 0;
  if (fileGid === myGid) return (mode & 0o010) !== 0;
  return (mode & 0o001) !== 0;
}

/**
 * validateCandidate runs SR-1.2 checks.  Returns the canonicalized absolute
 * path on success.  Throws ErrSystemInstallUnreachable on validation failure.
 */
function validateCandidate(candidatePath: string): string {
  let st: Stats;
  try {
    st = statSync(candidatePath);
  } catch {
    // The candidate path was supposed to exist (caller already stat'd it),
    // so a stat failure here means it disappeared between checks; treat as
    // not-a-regular-file to avoid leaking the raw errno.
    throw new ErrSystemInstallUnreachable(candidatePath, "not-a-regular-file");
  }

  if (!st.isFile()) {
    throw new ErrSystemInstallUnreachable(candidatePath, "not-a-regular-file");
  }

  if (!isExecutableFor(st)) {
    throw new ErrSystemInstallUnreachable(candidatePath, "not-executable");
  }

  // Canonicalize (chase symlinks) to honor SR-1.5's "always absolute" rule.
  let canonical: string;
  try {
    canonical = realpathSync(candidatePath);
  } catch {
    // Symlink loop or similar — surface as not-a-regular-file (the resolved
    // target is not navigable as a normal file).
    throw new ErrSystemInstallUnreachable(candidatePath, "not-a-regular-file");
  }

  // Re-verify after canonicalization in case the symlink target is a
  // directory or non-executable.
  try {
    const stTarget = statSync(canonical);
    if (!stTarget.isFile()) {
      throw new ErrSystemInstallUnreachable(canonical, "not-a-regular-file");
    }
    if (!isExecutableFor(stTarget)) {
      throw new ErrSystemInstallUnreachable(canonical, "not-executable");
    }
  } catch (e) {
    if (e instanceof ErrSystemInstallUnreachable) throw e;
    throw new ErrSystemInstallUnreachable(canonical, "not-a-regular-file");
  }

  // accessSync is a final belt-and-suspenders check against ACLs etc; if the
  // stat-based check said executable but accessSync says no, surface as
  // not-executable.
  try {
    accessSync(canonical, fsConstants.X_OK);
  } catch {
    throw new ErrSystemInstallUnreachable(canonical, "not-executable");
  }

  return canonical;
}

/**
 * canonicalizePath canonicalizes a candidate path the way SR-1.1 step 2
 * canonicalizes PATH lookups.  Used by the _cliPath DI hook (SR-4.1) so the
 * "binaryPath is unconditionally absolute" invariant holds in tests too.
 *
 * Returns the canonicalized absolute path on success; throws
 * ErrSystemInstallUnreachable on canonicalization failure.
 */
export function canonicalizePath(candidatePath: string): string {
  if (!isAbsolute(candidatePath)) {
    // Resolve against cwd first so realpath has an absolute base.
    candidatePath = resolve(candidatePath);
  }
  try {
    return realpathSync(candidatePath);
  } catch {
    throw new ErrSystemInstallUnreachable(candidatePath, "not-a-regular-file");
  }
}

/**
 * discoverSystemBinary runs SR-1.1 + SR-1.2 against the supplied env.
 *
 * Returns a validated candidate (canonicalized absolute path + which kind of
 * lookup produced it) on success.  Rejects with ErrSystemInstallNotFound when
 * no candidate exists; rejects with ErrSystemInstallUnreachable when the
 * standard-install-path candidate exists but fails validation (no fallthrough
 * to PATH, per SR-1.2).
 *
 * The env arg is injectable so tests can drive HOME/PATH variants without
 * mutating process.env.
 */
export function discoverSystemBinary(env: {
  HOME: string | undefined;
  PATH: string | undefined;
}): DiscoveredCandidate {
  const checkedLocations: CheckedLocation[] = [];

  // Step 1: standard install path
  const standardPath = resolveStandardInstallPath(env.HOME);
  if (standardPath !== null) {
    let exists = false;
    try {
      // statSync throws when the path doesn't exist; existsSync would be
      // tighter but we need to handle the realpath case below.
      statSync(standardPath);
      exists = true;
    } catch {
      // Candidate path computable but not present on disk — record and move on.
    }
    if (exists) {
      // SR-1.2: candidate at the standard install path exists.  Validate it
      // here; on failure surface ErrSystemInstallUnreachable WITHOUT falling
      // through to PATH (a present-but-broken standard install is operator
      // intent and must not be silently shadowed).
      const canonical = validateCandidate(standardPath);
      return { path: canonical, kind: "standard-install-path" };
    }
    checkedLocations.push({
      kind: "standard-install-path",
      detail: standardPath,
    });
  } else {
    checkedLocations.push({
      kind: "standard-install-path",
      detail: null,
    });
  }

  // Step 2: PATH lookup
  const pathCandidate = pathLookup(env.PATH);
  if (pathCandidate !== null) {
    const canonical = validateCandidate(pathCandidate);
    return { path: canonical, kind: "path-lookup" };
  }
  checkedLocations.push({
    kind: "path-lookup",
    detail: env.PATH ?? null,
  });

  throw new ErrSystemInstallNotFound(checkedLocations);
}

// Re-export the relative constant so test helpers can reference it without
// hardcoding the literal.
export { STANDARD_INSTALL_PATH_RELATIVE };

// Re-export dirname for callers that need to extract the bin dir without
// pulling node:path themselves.
export { dirname };
