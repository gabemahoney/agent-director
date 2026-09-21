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

// ── Sandbox guard (b.nh2 / absorbed b.4v7) ─────────────────────────────────
// These tests build and exec the agent-director binary, which can open (and,
// on a schema-bumping branch, migrate) the real ~/.agent-director on the host
// (b.8dr). The `make test-sandbox` targets run inside a container whose HOME
// has no .agent-director and set AGENT_DIRECTOR_TEST_SANDBOX=1. Refuse to run —
// before any build spawns below — if that marker is absent. Accident-prevention
// gate, not a security boundary.
if (!process.env.AGENT_DIRECTOR_TEST_SANDBOX) {
  console.error(
    "refusing to run: bun test must run via `make test-sandbox` (see the run-tests skill) — it can rewrite the real ~/.agent-director otherwise"
  );
  process.exit(1);
}

// The repo root is three levels above this file:
//   test/setup.ts → test/ → pkg/ts-bun-client/ → pkg/ → (repo root)
const repoRoot = resolve(import.meta.dir, "../../..");
const helperBin = resolve(repoRoot, "bin/ts-helper");
const fakeTmuxDir = resolve(repoRoot, "test/fake-tmux");
const cliBin = resolve(repoRoot, "bin/agent-director");

// ── Seeds flock (b.3jn / b.2y5 seeds-flock protocol) ───────────────────────
// The coverage gates run concurrently in one container. Each `make` below is a
// cross-package builder that reads walk-reachable tree sources (pkg/api/apitest
// among them). Sibling gate coverage.go-root's synthetic-regression test
// helper-tag-replay MUTATES pkg/api/apitest/seeds.go under an exclusive flock
// on pkg/api/apitest/.seeds-mutation.lock (b.2y5's acquireSeedsLock, LOCK_EX).
// Without the same lock, a make here can compile mid-mutation and fail (observed:
// "seeds.go:222: assignment mismatch: 1 variable but SeedSpawn returns 2 values").
// Invariant: any cross-package reader/builder of walk-reachable tree sources
// must hold the seeds flock while reading. We use /usr/bin/flock (util-linux,
// present in the sandbox image); it creates the lock file if missing and blocks
// until free — the same file and same flock(2) semantics as acquireSeedsLock, so
// these builds and go-root's mutators mutually exclude. One short flock per make
// call (three holds), not one long hold.
const seedsLockPath = resolve(repoRoot, "pkg/api/apitest/.seeds-mutation.lock");
// flock is util-linux and does NOT exist on darwin; this file is the bun test
// preload, so an unconditional `flock` spawn would ENOENT the whole suite before
// any test runs (the suite explicitly supports darwin via skip patterns). The
// cross-gate mutator we're locking against (coverage.go-root) only exists in the
// Linux sandbox, so where flock is absent the lock is unnecessary — fall back to
// a plain `make` invocation.
const hasFlock = Bun.which("flock") !== null;
const flockMake = (target: string) =>
  Bun.spawnSync(
    hasFlock
      ? ["flock", seedsLockPath, "make", "-C", repoRoot, target]
      : ["make", "-C", repoRoot, target],
    { stdout: "inherit", stderr: "inherit" }
  );

// ── ts-helper ─────────────────────────────────────────────────────────────
const helperProc = flockMake("ts-helper");

if (helperProc.exitCode !== 0) {
  console.error(
    `[setup] make ts-helper failed (exit ${helperProc.exitCode}); cannot run smoke tests.`
  );
  process.exit(1);
}

// ── fake-tmux ─────────────────────────────────────────────────────────────
const tmuxProc = flockMake("fake-tmux");

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
const cliProc = flockMake("agent-director");

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
