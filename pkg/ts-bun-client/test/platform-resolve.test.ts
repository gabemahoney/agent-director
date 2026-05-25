/**
 * platform-resolve.test.ts — unit tests for src/internal/platformResolve.ts (Task A2).
 *
 * Tests the three resolution paths defined in SRD SR-2.1 – SR-2.3:
 *   1. Success — resolves to an existing executable binary path on the current platform.
 *   2. Missing package — require.resolve throws → ErrPlatformPackageMissing thrown,
 *      message includes the platform package name.
 *   3. Missing execute bit — file exists but is not executable → ErrCliNotExecutable thrown.
 *
 * IMPORT NOTE: The module exports a testable overload alongside the public
 * `resolveCliPath()` function. Expected signature (engineer confirms exact name):
 *
 *   resolveCliPath(opts?: ResolveCliPathOpts): string
 *
 * where ResolveCliPathOpts may include:
 *   platform?: string           — override process.platform for testing
 *   arch?: string               — override process.arch for testing
 *   _requireResolve?: (pkg: string) => string   — DI hook for testing package lookup
 *
 * If the engineer uses a different internal-overload name (e.g. _resolveCliPathInternal),
 * update the import below and any constructor calls accordingly.
 */

import { test, expect, describe, afterEach } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import {
  resolveCliPath,
  ErrPlatformPackageMissing,
  ErrCliNotExecutable,
} from "../src/internal/platformResolve.js";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Absolute path to the in-repo built CLI binary (used in happy-path test). */
const repoRoot = path.resolve(import.meta.dir, "../../..");
const inRepoCli = path.resolve(repoRoot, "bin/agent-director");

const tmpDirs: string[] = [];
function makeTmpDir(): string {
  const d = fs.mkdtempSync(path.join(os.tmpdir(), "ad-prt-"));
  tmpDirs.push(d);
  return d;
}

afterEach(() => {
  // Clean up temp dirs created during tests.
  for (const d of tmpDirs.splice(0)) {
    try { fs.rmSync(d, { recursive: true, force: true }); } catch { /* ignore */ }
  }
});

// ---------------------------------------------------------------------------
// Case 1: Success — resolves to an executable binary path
// ---------------------------------------------------------------------------
describe("platform-resolve — success path", () => {
  test("returns a non-empty string path to an executable binary (linux/x64)", () => {
    // Skip on non-linux/x64 platforms that won't have the in-repo binary.
    if (process.platform !== "linux" || process.arch !== "x64") {
      console.log(
        `platform-resolve.test: skipping happy-path (not linux/x64; got ${process.platform}/${process.arch})`
      );
      return;
    }
    if (!fs.existsSync(inRepoCli)) {
      console.log(
        `platform-resolve.test: skipping happy-path (bin/agent-director not built; run make agent-director)`
      );
      return;
    }

    let cliPath: string;
    // resolveCliPath() uses require.resolve by default; on CI where the
    // optional sub-packages are installed, this should just work.
    // If it throws ErrPlatformPackageMissing (package not installed),
    // we fall back to injecting the in-repo path via the DI overload.
    try {
      cliPath = resolveCliPath();
    } catch {
      // Try the DI overload if the engineer exposed one.
      // Engineer: if your function signature differs, adjust this call.
      cliPath = resolveCliPath({
        _requireResolve: (_pkg: string) => inRepoCli,
      } as Parameters<typeof resolveCliPath>[0]);
    }

    expect(typeof cliPath).toBe("string");
    expect(cliPath.length).toBeGreaterThan(0);
    expect(fs.existsSync(cliPath)).toBe(true);

    // Must be executable (owner/group/other exec bit).
    const stat = fs.statSync(cliPath);
    const isExecutable = (stat.mode & 0o111) !== 0;
    expect(isExecutable).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Case 2: Missing package — require.resolve throws → ErrPlatformPackageMissing
// ---------------------------------------------------------------------------
describe("platform-resolve — missing package", () => {
  test("require.resolve failure → throws ErrPlatformPackageMissing", () => {
    // Inject a require.resolve stub that always throws (simulates uninstalled
    // optional dependency).
    const pkgName = "@agent-director/linux-x64";
    let caught: unknown;
    try {
      resolveCliPath({
        _requireResolve: (_pkg: string) => {
          throw new Error(`Cannot find module '${pkgName}'`);
        },
      } as Parameters<typeof resolveCliPath>[0]);
    } catch (e) {
      caught = e;
    }

    expect(caught).toBeInstanceOf(ErrPlatformPackageMissing);
    const err = caught as InstanceType<typeof ErrPlatformPackageMissing>;
    expect(err.errName).toBe("ErrPlatformPackageMissing");
    // The error must include the expected platform-package name somewhere.
    // Engineer: the exact package name in the message may vary; adjust if needed.
    const hasPackageHint =
      err.message.includes("@agent-director") ||
      err.message.includes("linux-x64") ||
      err.message.includes("darwin-arm64") ||
      err.errDescription.includes("@agent-director");
    expect(hasPackageHint).toBe(true);
  });
});

// ---------------------------------------------------------------------------
// Case 3: Missing execute bit → ErrCliNotExecutable
// ---------------------------------------------------------------------------
describe("platform-resolve — non-executable binary", () => {
  test("chmod -x temp file → throws ErrCliNotExecutable", () => {
    // Create a real file with no execute bits.
    const dir = makeTmpDir();
    const fakeBin = path.join(dir, "agent-director");
    fs.writeFileSync(fakeBin, "#!/bin/sh\necho ok\n", { mode: 0o644 }); // rw-r--r--
    // Explicitly strip execute bits (belt + suspenders in case the fs default added one).
    fs.chmodSync(fakeBin, 0o644);

    let caught: unknown;
    try {
      resolveCliPath({
        _requireResolve: (_pkg: string) => fakeBin,
      } as Parameters<typeof resolveCliPath>[0]);
    } catch (e) {
      caught = e;
    }

    expect(caught).toBeInstanceOf(ErrCliNotExecutable);
    const err = caught as InstanceType<typeof ErrCliNotExecutable>;
    expect(err.errName).toBe("ErrCliNotExecutable");
    // Error message must point at the path.
    expect(err.message).toContain(fakeBin);
  });
});
