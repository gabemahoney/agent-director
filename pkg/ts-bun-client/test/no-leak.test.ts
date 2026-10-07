/**
 * no-leak.test.ts — SR-10.5 (Linux-only): 1000 sequential Client calls leave no
 * agent-director child behind.
 *
 * b.3jn: the count is scoped to direct children of this process
 * (`pgrep -c -P <pid>`), so sibling release gates' agent-director runs do not
 * perturb it; a leaked call would be an unreaped child of this process, which
 * the scoped count sees. The two scoping tests below prove the counter catches
 * a real child and ignores a non-child.
 */

import { test, expect, afterEach } from "bun:test";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { homeStore, openClient } from "./internal/helper.js";
import { withTempHome } from "./internal/tempHome.js";

const isLinux = process.platform === "linux";

/** agent-director processes whose parent is ppid (default: this process). */
function childCount(ppid: number = process.pid): number {
  const proc = Bun.spawnSync({ cmd: ["pgrep", "-c", "-P", String(ppid), "agent-director"], stdout: "pipe" });
  const n = parseInt(new TextDecoder().decode(proc.stdout).trim(), 10);
  return Number.isFinite(n) ? n : 0;
}

test.skipIf(!isLinux)(
  "no-leak: 1000 sequential list({state: 'check_permission'}) calls leak zero processes",
  async () => {
    await withTempHome(async (homeDir) => {
      const baseline = childCount();
      using client = await openClient(homeStore(homeDir));
      for (let i = 0; i < 1000; i++) await client.list({ state: ["check_permission"] });
      expect(childCount()).toBe(baseline);
    });
  },
  180_000
);

// ── b.3jn: the scoped counter ────────────────────────────────────────────────

const spawned: Array<import("bun").Subprocess> = [];
const fakes: string[] = [];

/** A copy of /bin/sleep named agent-director, in a temp dir. */
function makeFake(): string {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "b3jn-fake-"));
  const fake = path.join(dir, "agent-director");
  fs.copyFileSync("/bin/sleep", fake);
  fs.chmodSync(fake, 0o755);
  fakes.push(fake);
  return fake;
}

/** Polls fn for up to 5 s. */
async function pollUntil(fn: () => boolean, message: string): Promise<void> {
  for (let waited = 0; waited < 5000; waited += 50) {
    if (fn()) return;
    await Bun.sleep(50);
  }
  if (!fn()) throw new Error(`pollUntil timed out: ${message}`);
}

afterEach(async () => {
  // pkill the fakes before their keeper, so the keeper reaps them: an orphan would
  // re-parent onto this process (PID 1 in the sandbox) as an agent-director zombie.
  for (const fake of fakes) Bun.spawnSync({ cmd: ["pkill", "-9", "-f", fake] });
  for (const proc of spawned) proc.kill(9);
  for (const proc of spawned) await proc.exited;
  for (const fake of fakes) fs.rmSync(path.dirname(fake), { recursive: true, force: true });
  spawned.length = fakes.length = 0;
});

test.skipIf(!isLinux)("leak-catch: the scoped count rises for a child and returns to baseline after reap", async () => {
  const baseline = childCount();
  const proc = Bun.spawn({ cmd: [makeFake(), "30"], stdout: "ignore", stderr: "ignore" });
  spawned.push(proc);
  await pollUntil(() => childCount() === baseline + 1, "the child fake never appeared");
  proc.kill(9);
  await proc.exited;
  await pollUntil(() => childCount() === baseline, "the count never returned to baseline after reap");
});

test.skipIf(!isLinux)("sibling-immunity: the scoped count ignores a non-child the global count sees", async () => {
  const before = childCount();
  // A bash keeper (not named agent-director) runs the fake as its own child and waits.
  const keeper = Bun.spawn({ cmd: ["bash", "-c", '"$1" 15 & wait', "_", makeFake()], stdout: "ignore", stderr: "ignore" });
  spawned.push(keeper);
  await pollUntil(() => childCount(keeper.pid) === 1, "the keeper's child fake never appeared");
  const global = Bun.spawnSync({ cmd: ["pgrep", "-c", "agent-director"], stdout: "pipe" });
  expect(parseInt(new TextDecoder().decode(global.stdout), 10)).toBeGreaterThan(0);
  expect(childCount()).toBe(before);
});
