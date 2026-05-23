/**
 * Bun test preload — builds bin/ts-helper (incrementally) before any test
 * runs and exports its absolute path via TS_HELPER_PATH.
 *
 * Loaded by Bun's test runner via bunfig.toml:
 *   [test]
 *   preload = ["./test/setup.ts"]
 *
 * Contract:
 *   - If `make ts-helper` exits non-zero the whole test run aborts.
 *   - After this module completes, process.env.TS_HELPER_PATH is the
 *     absolute path to bin/ts-helper; individual tests can shell out to it.
 *   - Subsequent `bun test` runs are fast because the make target is
 *     incremental (no-op when sources are unchanged).
 */

import { resolve } from "path";

// The repo root is three levels above this file:
//   test/setup.ts → test/ → pkg/ts-bun-client/ → pkg/ → (repo root)
const repoRoot = resolve(import.meta.dir, "../../..");
const helperBin = resolve(repoRoot, "bin/ts-helper");

const proc = Bun.spawnSync(["make", "-C", repoRoot, "ts-helper"], {
  stdout: "inherit",
  stderr: "inherit",
});

if (proc.exitCode !== 0) {
  console.error(
    `[setup] make ts-helper failed (exit ${proc.exitCode}); cannot run smoke tests.`
  );
  process.exit(1);
}

process.env.TS_HELPER_PATH = helperBin;
