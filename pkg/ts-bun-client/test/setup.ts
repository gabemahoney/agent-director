/**
 * Bun test preload — builds bin/ts-helper and test/fake-tmux/tmux
 * (incrementally) before any test runs.
 *
 * Loaded by Bun's test runner via bunfig.toml:
 *   [test]
 *   preload = ["./test/setup.ts"]
 *
 * Contract:
 *   - If `make ts-helper` exits non-zero the whole test run aborts.
 *   - After this module completes, process.env.TS_HELPER_PATH is the
 *     absolute path to bin/ts-helper; individual tests can shell out to it.
 *   - process.env.FAKE_TMUX_DIR is the directory containing the fake tmux
 *     binary; withTempHome prepends it to PATH so spawn/send-keys/etc. hit
 *     the stub instead of the real tmux.
 *   - Subsequent `bun test` runs are fast because the make targets are
 *     incremental (no-op when sources are unchanged).
 */

import { resolve } from "path";

// The repo root is three levels above this file:
//   test/setup.ts → test/ → pkg/ts-bun-client/ → pkg/ → (repo root)
const repoRoot = resolve(import.meta.dir, "../../..");
const helperBin = resolve(repoRoot, "bin/ts-helper");
const fakeTmuxDir = resolve(repoRoot, "test/fake-tmux");

// ── ts-helper ─────────────────────────────────────────────────────────────
const helperProc = Bun.spawnSync(["make", "-C", repoRoot, "ts-helper"], {
  stdout: "inherit",
  stderr: "inherit",
});

if (helperProc.exitCode !== 0) {
  console.error(
    `[setup] make ts-helper failed (exit ${helperProc.exitCode}); cannot run smoke tests.`
  );
  process.exit(1);
}

// ── fake-tmux ─────────────────────────────────────────────────────────────
const tmuxProc = Bun.spawnSync(["make", "-C", repoRoot, "fake-tmux"], {
  stdout: "inherit",
  stderr: "inherit",
});

if (tmuxProc.exitCode !== 0) {
  console.error(
    `[setup] make fake-tmux failed (exit ${tmuxProc.exitCode}); smoke tests that call tmux will fail.`
  );
  process.exit(1);
}

process.env.TS_HELPER_PATH = helperBin;
process.env.FAKE_TMUX_DIR = fakeTmuxDir;
