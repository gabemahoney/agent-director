/**
 * client.ts — public Client class + standalone resolveSystemBinary().
 *
 * Construction (SR-4.1): `Client.create(opts)` is the only sanctioned entry
 * point. The runtime constructor is private — `new Client(...)` is a
 * compile-time TS error.
 *
 * The factory runs the SR-1 discovery pipeline (HOME/standard-install-path →
 * PATH lookup), validates the candidate, probes its version, compares
 * against MIN_BINARY_VERSION, and only then allocates the underlying
 * SubprocessClient. Each failure mode surfaces as one of:
 *   - ErrBunVersionTooOld          (Bun runtime below MIN_BUN_VERSION)
 *   - ErrSystemInstallNotFound     (no candidate exists)
 *   - ErrSystemInstallTooOld       (candidate is below floor)
 *   - ErrSystemInstallUnreachable  (candidate failed validation or probe)
 *   - ErrCallerCwdUnreachable      (caller's process.cwd() is gone — b.cot)
 *
 * The DI hatch `_cliPath` (SR-4.1) skips discovery, canonicalizes the
 * injected path, and runs the probe so that `binaryPath` and `binaryVersion`
 * always carry production-shape values. `_cliPath` is not on the public
 * `ClientOptions` interface — it lives on the runtime object only.
 */

import { statSync } from "node:fs";
import {
  discoverSystemBinary,
  canonicalizePath,
} from "./internal/discovery.js";
import { runProbe } from "./internal/probe.js";
import { compareParsed, parseVersion } from "./internal/semver.js";
import { MIN_BINARY_VERSION } from "./internal/constants.js";
import { MIN_BUN_VERSION, checkBunVersion } from "./internal/platformResolve.js";
import { SubprocessClient } from "./internal/subprocessClient.js";
import {
  AgentDirectorError,
  ErrClientClosed,
  ErrSystemInstallTooOld,
  ErrSystemInstallUnreachable,
  ErrCallerCwdUnreachable,
} from "./errors.js";
import type { ClientOptions } from "./types.js";

function assertCallerCwdReachable(): void {
  let cwd: string | undefined;
  try {
    cwd = process.cwd();
    const st = statSync(cwd);
    if (!st.isDirectory()) throw new Error("not a directory");
  } catch (err) {
    if (err instanceof ErrCallerCwdUnreachable) throw err;
    throw new ErrCallerCwdUnreachable(
      typeof cwd === "string" ? cwd : "<process.cwd() unavailable>",
      err,
    );
  }
}

// Re-export so existing imports `import { ... } from "./client.js"` keep working.
export { AgentDirectorError, ErrClientClosed };
export type { ClientOptions };

/**
 * Public Client surface — drives the agent-director CLI binary via one
 * subprocess per verb call. Construct via `Client.create(opts)`; the
 * runtime constructor is private (SR-4.1).
 */
export class Client extends SubprocessClient {
  /**
   * Async factory — runs SR-1 discovery + SR-2.3 floor comparison, then
   * allocates the underlying SubprocessClient.
   *
   * Rejects with:
   *   - ErrBunVersionTooOld
   *   - ErrSystemInstallNotFound
   *   - ErrSystemInstallTooOld
   *   - ErrSystemInstallUnreachable
   *   - ErrCallerCwdUnreachable  (b.cot — caller's process.cwd() is gone)
   */
  static async create(opts: ClientOptions = {}): Promise<Client> {
    checkBunVersion();

    const opts2 = opts as ClientOptions & { _cliPath?: string };
    let binaryPath: string;
    if (opts2._cliPath !== undefined) {
      binaryPath = canonicalizePath(opts2._cliPath);
    } else {
      const candidate = discoverSystemBinary({
        HOME: process.env["HOME"],
        PATH: process.env["PATH"],
      });
      binaryPath = candidate.path;
    }

    const probe = await runProbe(binaryPath);
    if (!probe.ok) {
      throw new ErrSystemInstallUnreachable(binaryPath, probe.reason, {
        diagnostic: probe.diagnostic,
        exitCode: probe.exitCode,
        signal: probe.signal,
      });
    }

    const probed = parseVersion(probe.version);
    if (!probed.ok) {
      throw new ErrSystemInstallUnreachable(binaryPath, "unparseable-version", {
        diagnostic: probe.version,
      });
    }
    const floor = parseVersion(MIN_BINARY_VERSION);
    if (!floor.ok) {
      throw new Error(
        `internal: MIN_BINARY_VERSION="${MIN_BINARY_VERSION}" failed strict SemVer parse`,
      );
    }
    if (compareParsed(probed.value, floor.value) < 0) {
      throw new ErrSystemInstallTooOld(probe.version, MIN_BINARY_VERSION, binaryPath);
    }

    // b.cot: fail-fast if caller's cwd is unreachable — AD subprocess calls
    // inherit cwd from the caller; a deleted cwd causes ENOENT on posix_spawn
    // that misleadingly blames the binary path rather than the cwd.
    assertCallerCwdReachable();

    return new Client(opts, {
      binaryPath,
      binaryVersion: probe.version,
    });
  }
}

/**
 * Result shape returned by resolveSystemBinary() (SR-4.3).
 */
export interface ResolveSystemBinaryResult {
  readonly path: string;
  readonly version: string;
}

/**
 * Options for resolveSystemBinary() (SR-4.3). Reserved for future expansion;
 * deliberately exposes no `_cliPath` analogue (SR-4.3 intentional asymmetry
 * with ClientOptions).
 */
export interface ResolveSystemBinaryOptions {
  // Reserved for future expansion. Currently has no documented fields.
}

/**
 * resolveSystemBinary — standalone discovery + probe entry point.
 *
 * Runs the same SR-1.1 → SR-2.3 pipeline as Client.create() and resolves
 * with `{ path, version }`. No Client is allocated, no cache is warmed.
 *
 * Rejects with the same error classes as Client.create():
 *   - ErrBunVersionTooOld
 *   - ErrSystemInstallNotFound
 *   - ErrSystemInstallTooOld
 *   - ErrSystemInstallUnreachable
 *   - ErrCallerCwdUnreachable  (b.cot — caller's process.cwd() is gone)
 */
export async function resolveSystemBinary(
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  _opts: ResolveSystemBinaryOptions = {},
): Promise<ResolveSystemBinaryResult> {
  checkBunVersion();

  const candidate = discoverSystemBinary({
    HOME: process.env["HOME"],
    PATH: process.env["PATH"],
  });
  const binaryPath = candidate.path;

  const probe = await runProbe(binaryPath);
  if (!probe.ok) {
    throw new ErrSystemInstallUnreachable(binaryPath, probe.reason, {
      diagnostic: probe.diagnostic,
      exitCode: probe.exitCode,
      signal: probe.signal,
    });
  }

  const probed = parseVersion(probe.version);
  if (!probed.ok) {
    throw new ErrSystemInstallUnreachable(binaryPath, "unparseable-version", {
      diagnostic: probe.version,
    });
  }
  const floor = parseVersion(MIN_BINARY_VERSION);
  if (!floor.ok) {
    throw new Error(
      `internal: MIN_BINARY_VERSION="${MIN_BINARY_VERSION}" failed strict SemVer parse`,
    );
  }
  if (compareParsed(probed.value, floor.value) < 0) {
    throw new ErrSystemInstallTooOld(probe.version, MIN_BINARY_VERSION, binaryPath);
  }

  // b.cot: fail-fast if caller's cwd is unreachable — same hazard as Client.create().
  assertCallerCwdReachable();

  return { path: binaryPath, version: probe.version };
}

export { MIN_BUN_VERSION };
