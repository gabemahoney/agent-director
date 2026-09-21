/**
 * no-leak-scoping.test.ts — b.3jn falsifiability tests.
 *
 * Proves the parent-scoped process counter used by no-leak.test.ts both
 * CATCHES real leaked children and IGNORES sibling-gate spawns that merely
 * share the "agent-director" command name. These are the regression tests for
 * b.3jn: without the parent scope, sibling-immunity fails (a host-global count
 * sees the sibling); with it, it passes.
 *
 * These tests do NOT run the 1000-call loop — they only exercise the counter,
 * spawning a fake `agent-director` (a renamed copy of a long-running binary).
 * Every spawned fake is killed and reaped in an afterEach hook so the tests
 * leak nothing themselves, even on a failed assertion mid-test.
 *
 * Linux-only (pgrep + comm-name behaviour); skipped on darwin per SR-10.5.
 */

import { test, expect, afterEach } from "bun:test";
import * as os from "os";
import * as path from "path";
import * as fs from "fs";
import { countChildAgentDirectorProcesses } from "./internal/processCount.js";

const isLinux = process.platform === "linux";

/**
 * Parent-scoped counter under test — the exact function no-leak.test.ts uses,
 * imported (not copied) so the two files can never drift. See
 * ./internal/processCount.ts for the b.3jn scoping rationale.
 */
function scopedCount(ppid: number = process.pid): number {
  return countChildAgentDirectorProcesses(ppid);
}

/** Host-global count of agent-director processes (any parent). */
function globalCount(): number {
  const proc = Bun.spawnSync({
    cmd: ["pgrep", "-c", "agent-director"],
    stdout: "pipe",
    stderr: "pipe",
  });
  const out = new TextDecoder().decode(proc.stdout).trim();
  const n = parseInt(out, 10);
  return Number.isFinite(n) ? n : 0;
}

/** Creates a temp dir with a copy of /bin/sleep renamed to `agent-director`. */
function makeFakeBinary(): { dir: string; fakePath: string } {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "b3jn-fake-"));
  const fakePath = path.join(dir, "agent-director");
  fs.copyFileSync("/bin/sleep", fakePath);
  fs.chmodSync(fakePath, 0o755);
  return { dir, fakePath };
}

/**
 * Polls `fn` until it returns true or the bounded budget (5s) is exhausted,
 * failing with `message` on timeout. Replaces fixed settle sleeps that flake
 * under load.
 */
async function pollUntil(fn: () => boolean, message: string): Promise<void> {
  const deadlineMs = 5000;
  const stepMs = 50;
  for (let waited = 0; waited < deadlineMs; waited += stepMs) {
    if (fn()) return;
    await new Promise((r) => setTimeout(r, stepMs));
  }
  if (fn()) return;
  throw new Error(`pollUntil timed out after ${deadlineMs}ms: ${message}`);
}

// Track everything we create so afterEach can guarantee cleanup even on a
// failed assertion mid-test.
const spawned: Array<import("bun").Subprocess> = [];
const tempDirs: string[] = [];
const fakePaths: string[] = [];

afterEach(async () => {
  // Order matters (b.3jn): pkill the fakes FIRST, while their still-alive
  // keeper's `wait` can reap them, THEN kill the keeper. Killing the keeper
  // first would orphan the fake; in the sandbox the bun test process is PID 1,
  // so the orphan re-parents onto process.pid and — once pkilled — becomes an
  // unreapable zombie CHILD named agent-director, i.e. the very leak this file
  // claims cannot happen (and a cross-test flake if bun reaps it during
  // no-leak.test.ts's baseline→after window).
  for (const p of fakePaths) {
    Bun.spawnSync({ cmd: ["pkill", "-9", "-f", p], stdout: "ignore", stderr: "ignore" });
  }
  fakePaths.length = 0;
  // Now kill and reap the keepers / direct-child fakes.
  for (const proc of spawned) {
    try {
      proc.kill(9);
    } catch {
      /* already dead */
    }
  }
  for (const proc of spawned) {
    try {
      await proc.exited;
    } catch {
      /* already reaped */
    }
  }
  spawned.length = 0;
  for (const d of tempDirs) {
    try {
      fs.rmSync(d, { recursive: true, force: true });
    } catch {
      /* best-effort */
    }
  }
  tempDirs.length = 0;
});

test.skipIf(!isLinux)(
  "leak-catch: scoped counter rises for a child fake and returns to baseline after reap",
  async () => {
    const { dir, fakePath } = makeFakeBinary();
    tempDirs.push(dir);
    fakePaths.push(fakePath);

    const baseline = scopedCount();

    // Spawn the fake as a DIRECT child of this process. Its comm becomes
    // "agent-director" (execve of a file named agent-director), so pgrep -P
    // ${process.pid} agent-director matches it.
    const proc = Bun.spawn({ cmd: [fakePath, "30"], stdout: "ignore", stderr: "ignore" });
    spawned.push(proc);

    // Poll until the exec lands in the process table and the scope sees it.
    await pollUntil(
      () => scopedCount() === baseline + 1,
      `child fake never appeared as a scoped child (scopedCount stuck at ${baseline})`
    );

    // Kill and reap — awaiting exited clears the entry from the child table.
    proc.kill(9);
    await proc.exited;
    spawned.length = 0;

    // Poll for the reaped child to disappear from pgrep.
    await pollUntil(
      () => scopedCount() === baseline,
      `scoped count never returned to baseline ${baseline} after reap`
    );
    expect(scopedCount()).toBe(baseline);
  }
);

test.skipIf(!isLinux)(
  "sibling-immunity: scoped counter ignores a non-child fake the global count sees",
  async () => {
    const { dir, fakePath } = makeFakeBinary();
    tempDirs.push(dir);
    fakePaths.push(fakePath);

    const scopedBefore = scopedCount();

    // Spawn the fake as a NON-child of THIS process by interposing a long-lived
    // intermediate shell as its parent. The keeper shell is a direct child of
    // this process, but its comm is "bash", NOT "agent-director", so
    // `pgrep -P ${process.pid} agent-director` never matches it. The keeper does
    // NOT exec the fake (exec would replace the shell and give the fake the
    // shell's pid, making it a direct child). Instead the keeper launches the
    // fake as ITS OWN child and then `wait`s. So the fake's parent is the
    // keeper's pid, which differs from process.pid. This models a sibling
    // coverage gate's spawn: a real agent-director whose parent is that gate,
    // not the bun test process.
    //
    // We deliberately do NOT orphan the fake to PID 1. In the sandbox the bun
    // test process IS PID 1, so an orphaned agent-director would re-parent
    // straight back onto process.pid and the scope would (correctly for that
    // topology) count it. Keeping a live non-bun parent models the real
    // coverage-phase topology, where the bun process is never PID 1.
    const keeper = Bun.spawn({
      cmd: ["bash", "-c", '"$1" 15 & wait', "_", fakePath],
      stdout: "ignore",
      stderr: "ignore",
      stdin: "ignore",
    });
    // Track the keeper so afterEach reaps it; the pkill-by-path in afterEach
    // reaps the fake first (see the ordering note in afterEach).
    spawned.push(keeper);

    // Poll until the fake is a live DIRECT child of the KEEPER (parent =
    // keeper.pid). Asserting the fake directly — rather than that the global
    // count merely rose above a scoped baseline — is what makes the immunity
    // assertion non-vacuous: an unrelated host agent-director could satisfy a
    // global>scoped precondition even if this fake never spawned.
    await pollUntil(
      () => scopedCount(keeper.pid) === 1,
      `keeper's child fake never appeared (pgrep -P ${keeper.pid} agent-director != 1)`
    );

    // The fake is genuinely alive and visible host-globally (the signal the old
    // host-global counter would have — wrongly — attributed to no-leak).
    expect(globalCount()).toBeGreaterThan(0);

    // The bug's regression assertion: the SCOPED counter for THIS process does
    // NOT include the sibling (its parent is the keeper, not process.pid). With
    // the OLD host-global logic this equality would fail because the global
    // count rose by the fake; with parent scoping it holds.
    expect(scopedCount()).toBe(scopedBefore);
  }
);

test.skipIf(isLinux)(
  "no-leak-scoping: skipped on darwin (SR-10.5 Linux-only)",
  () => {
    expect(true).toBe(true);
  }
);
