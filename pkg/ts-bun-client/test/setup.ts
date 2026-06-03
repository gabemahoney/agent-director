/**
 * Bun test preload — builds bin/ts-helper, test/fake-tmux/tmux, and
 * bin/agent-director (incrementally) before any test runs.
 *
 * Loaded by Bun's test runner via bunfig.toml:
 *   [test]
 *   preload = ["./test/setup.ts"]
 *
 * Contract:
 *   - If any make target exits non-zero the whole test run aborts.
 *   - After this module completes, process.env.TS_HELPER_PATH is the
 *     absolute path to bin/ts-helper; individual tests can shell out to it.
 *   - process.env.FAKE_TMUX_DIR is the directory containing the fake tmux
 *     binary; withTempHome prepends it to PATH so spawn/send-keys/etc. hit
 *     the stub instead of the real tmux.
 *   - process.env.CLI_PATH is the absolute path to bin/agent-director; the
 *     envelope-diff tests use it to spawn CLI subprocesses.
 *   - Subsequent `bun test` runs are fast because the make targets are
 *     incremental (no-op when sources are unchanged).
 */

import { resolve } from "path";
import { chmodSync } from "fs";

// The repo root is three levels above this file:
//   test/setup.ts → test/ → pkg/ts-bun-client/ → pkg/ → (repo root)
const repoRoot = resolve(import.meta.dir, "../../..");
const helperBin = resolve(repoRoot, "bin/ts-helper");
const fakeTmuxDir = resolve(repoRoot, "test/fake-tmux");
const cliBin = resolve(repoRoot, "bin/agent-director");

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

// Enforce executable bit on the fake-tmux stub. The Makefile recipe now does
// this too (belt-and-suspenders), but if the binary lands at 644 by any means
// (manual chmod, copy without x-bit, etc.) exec.LookPath will skip it and
// fall through to the real /usr/bin/tmux, leaking real tmux sessions.
chmodSync(resolve(fakeTmuxDir, "tmux"), 0o755);

// ── agent-director CLI binary ─────────────────────────────────────────────
// `make agent-director` is an alias for `make build`; it is incremental and
// fast when sources are unchanged.  Required by the envelope-diff tests that
// spawn the real CLI as a subprocess.
const cliProc = Bun.spawnSync(["make", "-C", repoRoot, "agent-director"], {
  stdout: "inherit",
  stderr: "inherit",
});

if (cliProc.exitCode !== 0) {
  console.error(
    `[setup] make agent-director failed (exit ${cliProc.exitCode}); envelope-diff tests will fail.`
  );
  process.exit(1);
}

process.env.TS_HELPER_PATH = helperBin;
process.env.FAKE_TMUX_DIR = fakeTmuxDir;
process.env.CLI_PATH = cliBin;

// b.ue3 / Epic 4: the pkg/ts-bun-client/platforms/ subtree is gone.
// Tests reach the dev binary via process.env.CLI_PATH (the `_cliPath`
// DI hatch on Client.create) — no platform-staging needed.
