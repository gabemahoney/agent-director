/**
 * verify-installed-pkg.test.ts — integration test for the verify-installed-pkg.ts driver (B3).
 *
 * This is a thin integration test of the DRIVER SHAPE only.
 * It does NOT verify the smoke's assertion content (e.g. make-template
 * overwrite behaviour) — that is Epic D's scope (Task D1).
 *
 * What is tested here:
 *   1. Happy path: spawning the script with AD_VERIFY_AGAINST pointing at the
 *      in-repo built CLI binary exits 0 and emits recognizable output on
 *      stdout or stderr.
 *   2. Error path: spawning the script with AD_VERIFY_AGAINST pointing at a
 *      non-executable file exits non-zero and emits a descriptive message on
 *      stderr (not a raw JS stack trace / unhandled rejection).
 *
 * AD_VERIFY_AGAINST is the test-injection env var that overrides the normal
 * "resolve from installed node_modules" logic so the script can be exercised
 * in a dev/CI context without a published tarball install.
 */

import { test, expect, describe } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";

// ---------------------------------------------------------------------------
// Paths
// ---------------------------------------------------------------------------

// Root of the ts-bun-client package (where package.json lives).
const PKG_ROOT = path.resolve(import.meta.dir, "..");

// In-repo CLI binary — used as AD_VERIFY_AGAINST for the happy-path case.
// Prefer the dev-build at bin/agent-director; fall back to dist/ cross-compiled
// linux binary if available.
const REPO_ROOT = path.resolve(PKG_ROOT, "../../..");
const BINARY_CANDIDATES = [
  path.join(REPO_ROOT, "bin", "agent-director"),
  path.join(REPO_ROOT, "dist", `agent-director-linux-amd64`),
  path.join(REPO_ROOT, "dist", `agent-director-linux-arm64`),
];
const BINARY_PATH = BINARY_CANDIDATES.find((p) => {
  try {
    fs.accessSync(p, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
});

// ---------------------------------------------------------------------------
// Helper: run the script and capture stdout/stderr/exitCode
// ---------------------------------------------------------------------------
interface ScriptResult {
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

async function runSmoke(
  extraEnv: Record<string, string> = {}
): Promise<ScriptResult> {
  const proc = Bun.spawn(
    ["bun", "run", "scripts/verify-installed-pkg.ts", "--smoke"],
    {
      cwd: PKG_ROOT,
      env: {
        ...process.env,
        ...extraEnv,
      },
      stdout: "pipe",
      stderr: "pipe",
      stdin: "ignore",
    }
  );

  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);

  await proc.exited;

  return {
    exitCode: proc.exitCode,
    stdout,
    stderr,
  };
}

// ---------------------------------------------------------------------------
// Test 1 — Happy path (AD_VERIFY_AGAINST = real executable CLI binary)
// ---------------------------------------------------------------------------
describe("verify-installed-pkg --smoke driver shape", () => {
  test(
    "happy path: exits 0 when AD_VERIFY_AGAINST points at real CLI binary",
    async () => {
      if (!BINARY_PATH) {
        console.warn(
          "verify-installed-pkg.test.ts: no executable CLI binary found in " +
            BINARY_CANDIDATES.join(", ") +
            " — skipping happy-path case"
        );
        return;
      }

      const result = await runSmoke({ AD_VERIFY_AGAINST: BINARY_PATH });

      expect(result.exitCode).toBe(0);

      // The script should produce SOME output (version line, success note, etc.)
      // We do not assert exact content here — that is Epic D's scope.
      const combinedOutput = result.stdout + result.stderr;
      expect(combinedOutput.length).toBeGreaterThan(0);

      // The combined output should NOT contain an unhandled-rejection or raw
      // Node/Bun JS error stack (a raw stack means the driver threw instead of
      // cleanly reporting success/failure).
      expect(combinedOutput).not.toMatch(
        /UnhandledPromiseRejection|at Object\.\<anonymous\>|Error: Cannot find module/
      );
    },
    30_000
  );

  // ---------------------------------------------------------------------------
  // Test 2 — Error path (AD_VERIFY_AGAINST = non-executable file)
  // ---------------------------------------------------------------------------
  test(
    "error path: exits non-zero when AD_VERIFY_AGAINST points at non-executable file",
    async () => {
      // Create a temp file that exists but is NOT executable.
      const tmpFile = path.join(os.tmpdir(), `not-executable-${Date.now()}.bin`);
      fs.writeFileSync(tmpFile, "I am not a binary");
      fs.chmodSync(tmpFile, 0o644); // readable but not executable

      try {
        const result = await runSmoke({ AD_VERIFY_AGAINST: tmpFile });

        // Must exit non-zero.
        expect(result.exitCode).not.toBe(0);

        // Must emit a descriptive message on stderr (not silent, not a raw stack).
        // The message should mention the path or "executable" or similar — i.e.,
        // the driver must translate ErrCliNotExecutable into a human-readable output.
        expect(result.stderr.length).toBeGreaterThan(0);
        expect(result.stderr).not.toMatch(/UnhandledPromiseRejection/);
      } finally {
        try {
          fs.unlinkSync(tmpFile);
        } catch {
          // best-effort cleanup
        }
      }
    },
    15_000
  );
});
