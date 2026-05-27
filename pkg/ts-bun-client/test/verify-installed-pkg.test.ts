/**
 * verify-installed-pkg.test.ts — integration test for the verify-installed-pkg.ts driver.
 *
 * This is a thin integration test of the DRIVER SHAPE only.
 * It does NOT verify detailed assertion content — that is Epic D's scope.
 *
 * What is tested here:
 *   1. --smoke happy path: exits 0 when Client module + CLI binary are available.
 *   2. --smoke error path: exits non-zero when AD_VERIFY_AGAINST is a bad module path.
 *   3. --full happy path: exits 0 when Client module + CLI binary are available.
 *   4. --smoke --full mutex: exits 1 with error on stderr.
 *   5. No-flag: exits 1 with usage message naming both --smoke and --full.
 *   6-9. Gauntlet FAIL-line shape (parameterized): one test per sub-step; each
 *        drives the driver into a failure via the shared fake-Client fixture and
 *        asserts the matching "FAIL <sub-step>" line on stderr.
 *
 * AD_VERIFY_AGAINST is the test-injection env var that overrides the normal
 * "resolve from installed node_modules" logic so the script can be exercised
 * in a dev/CI context without a published tarball install.
 *
 * AD_CLI_PATH is the companion env var that points the Client at the in-repo
 * CLI binary, bypassing platform-package resolution.
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

// Repo root: two levels up from pkg/ts-bun-client.
const REPO_ROOT = path.resolve(PKG_ROOT, "../..");

// In-repo TypeScript source module — used as AD_VERIFY_AGAINST so the driver
// loads the real Client class without needing a published tarball.
const SRC_INDEX = path.join(PKG_ROOT, "src/index.ts");

// Shared fake-Client fixture for gauntlet failure-injection tests (TB-S3).
const FAKE_CLIENT = path.join(import.meta.dir, "fixtures/fake-client/index.ts");

// In-repo CLI binary — needed as AD_CLI_PATH for happy-path tests so the
// Client can make real subprocess calls.
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
// Helpers
// ---------------------------------------------------------------------------

interface ScriptResult {
  exitCode: number | null;
  stdout: string;
  stderr: string;
}

async function runScript(
  args: string[],
  extraEnv: Record<string, string> = {}
): Promise<ScriptResult> {
  const proc = Bun.spawn(
    ["bun", "run", "scripts/verify-installed-pkg.ts", ...args],
    {
      cwd: PKG_ROOT,
      env: { ...process.env, ...extraEnv },
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

  return { exitCode: proc.exitCode, stdout, stderr };
}

function runSmoke(extraEnv: Record<string, string> = {}) {
  return runScript(["--smoke"], extraEnv);
}

function runFull(extraEnv: Record<string, string> = {}) {
  return runScript(["--full"], extraEnv);
}

// ---------------------------------------------------------------------------
// --smoke driver shape
// ---------------------------------------------------------------------------

describe("verify-installed-pkg --smoke driver shape", () => {
  test(
    "happy path: exits 0 when Client module + CLI binary are available",
    async () => {
      if (!BINARY_PATH) {
        console.warn(
          "verify-installed-pkg.test.ts: no executable CLI binary found in " +
            BINARY_CANDIDATES.join(", ") +
            " — skipping happy-path case"
        );
        return;
      }

      const result = await runSmoke({
        AD_VERIFY_AGAINST: SRC_INDEX,
        AD_CLI_PATH: BINARY_PATH,
      });

      expect(result.exitCode).toBe(0);

      const combinedOutput = result.stdout + result.stderr;
      expect(combinedOutput.length).toBeGreaterThan(0);
      expect(combinedOutput).not.toMatch(
        /UnhandledPromiseRejection|at Object\.<anonymous>|Error: Cannot find module/
      );
    },
    30_000
  );

  test(
    "error path: exits non-zero when AD_VERIFY_AGAINST points at non-executable file",
    async () => {
      const tmpFile = path.join(os.tmpdir(), `not-executable-${Date.now()}.bin`);
      fs.writeFileSync(tmpFile, "I am not a binary");
      fs.chmodSync(tmpFile, 0o644);

      try {
        const result = await runSmoke({ AD_VERIFY_AGAINST: tmpFile });

        expect(result.exitCode).not.toBe(0);
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

// ---------------------------------------------------------------------------
// --full driver shape
// ---------------------------------------------------------------------------

describe("verify-installed-pkg --full driver shape", () => {
  test(
    "happy path: exits 0 when Client module + CLI binary are available",
    async () => {
      if (!BINARY_PATH) {
        console.warn(
          "verify-installed-pkg.test.ts: no executable CLI binary found in " +
            BINARY_CANDIDATES.join(", ") +
            " — skipping happy-path case"
        );
        return;
      }

      const result = await runFull({
        AD_VERIFY_AGAINST: SRC_INDEX,
        AD_CLI_PATH: BINARY_PATH,
      });

      expect(result.exitCode).toBe(0);

      const combinedOutput = result.stdout + result.stderr;
      expect(combinedOutput.length).toBeGreaterThan(0);
      expect(combinedOutput).not.toMatch(
        /UnhandledPromiseRejection|at Object\.<anonymous>|Error: Cannot find module/
      );
    },
    30_000
  );

  test(
    "mutex: --smoke --full together exits 1 with error on stderr",
    async () => {
      const result = await runScript(["--smoke", "--full"]);

      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("mutually exclusive");
      expect(result.stderr).not.toMatch(/UnhandledPromiseRejection/);
    },
    10_000
  );

  test(
    "no-flag: exits 1 with usage message naming both --smoke and --full",
    async () => {
      const result = await runScript([]);

      expect(result.exitCode).toBe(1);
      expect(result.stderr).toContain("--smoke");
      expect(result.stderr).toContain("--full");
    },
    10_000
  );
});

// ---------------------------------------------------------------------------
// --full gauntlet FAIL-line shape (parameterized)
// ---------------------------------------------------------------------------

const GAUNTLET_STEPS = [
  "makeTemplate-create",
  "makeTemplate-collision",
  "makeTemplate-overwrite",
  "makeTemplate-reread",
] as const;

describe("verify-installed-pkg --full gauntlet FAIL-line shape", () => {
  for (const step of GAUNTLET_STEPS) {
    test(
      `FAIL ${step}: driver emits "FAIL ${step}" on stderr`,
      async () => {
        const result = await runFull({
          AD_VERIFY_AGAINST: FAKE_CLIENT,
          FAKE_CLIENT_FAIL_STEP: step,
        });

        expect(result.exitCode).not.toBe(0);
        expect(result.stderr).toContain(`FAIL ${step}`);
      },
      15_000
    );
  }
});
