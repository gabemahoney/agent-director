/**
 * Smoke test — expire verb
 *
 * Happy path: seed an ended spawn, call expire with older_than="0d" (zero
 * duration reaps ALL terminal rows regardless of ended_at). Asserts count >= 1.
 *
 * Error path: expire declares no ErrorNames in the manifest — per-row
 * failures surface in the result map, not as a verb-level error. This verb
 * is in the smoke-invariants allow-list for missing error-case tests.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client } from "../../src/index.js";
import type { ExpireResult } from "../../src/index.js";

test("expire: happy path — reaps ended rows with older_than=0d", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    const spawnId = "smoke-expire-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "ended",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true });
    // older_than="0d" → zero duration → reap ALL terminal rows.
    const result: ExpireResult = await client.expire({ older_than: "0d" });
    expect(typeof result.count).toBe("number");
    expect(result.count).toBeGreaterThanOrEqual(1);
    expect(Array.isArray(result.ids)).toBe(true);
    expect(result.ids).toContain(spawnId);
  });
}, 10_000);

// Error path: expire declares no ErrorNames in the manifest.
// No error-case test is included. See smoke-invariants.test.ts for the
// allow-list entry that exempts this verb.
