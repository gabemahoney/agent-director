// TODO(b.eiv Epic E): delete this file after FFI source removal.
// Skipped during Epic B cutover (b.19d t1.19d.9i) — exercises FFI-only internals
// that no longer reach the public surface.

/**
 * Off-main-thread dispatch test.
 *
 * Verifies that the worker processes FFI calls while the main thread is busy
 * (spinning in a busy-loop). This exercises the core architectural property:
 * verb calls run in a dedicated worker thread and the main event loop is not
 * the bottleneck.
 *
 * Setup:
 *   - Requires the compiled shared library (libagent_director.so).
 *     If absent, the test file exits 0 with a diagnostic (same pattern as
 *     client-lifecycle.test.ts).
 *
 * Test:
 *   1. Open a Client against a fresh temp store.
 *   2. Start a `version` verb call (handle-free, so no spawn needed).
 *   3. Immediately after starting the call, busy-loop the main thread for 50ms.
 *   4. Await the verb result.
 *   5. Assert the total elapsed time is ≤ 200ms (the call completed in
 *      parallel with the busy-loop, not after it).
 */

import { test, expect, describe, afterAll } from "bun:test";
import * as fs from "node:fs";
import * as path from "node:path";
import { Client } from "../src/client.js";
// shutdownWorker import removed — worker cleanup happens automatically on exit

// ---------------------------------------------------------------------------
// Pre-flight: verify the shared library is present
// ---------------------------------------------------------------------------
const repoRoot = path.resolve(import.meta.dir, "../../..");
const soPath = path.join(repoRoot, "dist", "libagent_director.so");

const soExists = fs.existsSync(soPath);
if (!soExists) {
  try {
    const proc = Bun.spawnSync(["make", "-C", repoRoot, "libagent_director"], {
      stdout: "pipe",
      stderr: "pipe",
    });
    if (proc.exitCode !== 0) {
      console.error(
        `off-main-thread.test.ts: libagent_director.so not found and 'make libagent_director' ` +
          `failed (exit ${proc.exitCode}). SKIPPING.`
      );
      process.exit(0);
    }
  } catch {
    console.error(
      `off-main-thread.test.ts: libagent_director.so not found and make is unavailable. SKIPPING.`
    );
    process.exit(0);
  }
}

// ---------------------------------------------------------------------------
// Temp store helpers
// ---------------------------------------------------------------------------
const tmpBase = path.join(import.meta.dir, ".tmp-omt");

function makeTmpDir(): string {
  const dir = path.join(tmpBase, `store-${Date.now()}-${Math.random().toString(36).slice(2)}`);
  fs.mkdirSync(dir, { recursive: true });
  return dir;
}

function removeTmpDir(dir: string): void {
  try {
    fs.rmSync(dir, { recursive: true, force: true });
  } catch {
    // best-effort cleanup
  }
}

afterAll(() => {
  removeTmpDir(tmpBase);
});

// ---------------------------------------------------------------------------
// Busy-loop helper
// ---------------------------------------------------------------------------

/** busyLoopMs spins synchronously for approximately `ms` milliseconds. */
function busyLoopMs(ms: number): void {
  const end = Date.now() + ms;
  while (Date.now() < end) {
    // tight spin — intentionally blocks the event loop
  }
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe.skip("off-main-thread worker dispatch", () => {
  test(
    "verb call completes while main thread is busy (not delayed by busy-loop)",
    async () => {
      const dir = makeTmpDir();
      let client: Client | undefined;
      try {
        const storePath = path.join(dir, "state.db");
        client = new Client({ storePath, createIfMissing: true });

        const start = Date.now();

        // Start the verb call (non-awaited Promise).
        const versionPromise = client.version({});

        // Immediately spin the main thread for 50ms — this should NOT delay
        // the worker's completion because the worker runs in a separate thread.
        busyLoopMs(50);

        // Now await the result.
        const result = await versionPromise;
        const elapsed = Date.now() - start;

        // The call should have completed in far less than 200ms total (the
        // worker was processing during our busy-loop, not waiting for it).
        expect(elapsed).toBeLessThan(200);

        // Sanity: the version verb returns a "version" field.
        expect(typeof (result as unknown as Record<string, unknown>).version).toBe("string");
      } finally {
        client?.close();
        removeTmpDir(dir);
      }
    },
    5000 // 5s timeout to allow first-time worker startup
  );
});
