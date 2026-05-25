/**
 * Smoke test — find-missing verb
 *
 * Happy path: call on an empty store. No spawns exist, so count=0 and ids=[].
 * The verb sweeps live spawns for missing tmux sessions; on an empty store
 * there is nothing to probe.
 *
 * Error path: find-missing's only documented error is ErrProbeUnsupported
 * (platform mismatch). On linux this cannot be triggered from a test
 * (the probe is always supported). This verb is in the smoke-invariants
 * allow-list for missing error-case tests.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { Client } from "../../src/index.js";
import type { FindMissingResult } from "../../src/index.js";

test("find-missing: happy path — empty store returns count=0", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = new Client({ storePath, createIfMissing: true });
    const result: FindMissingResult = await client.findMissing({});
    expect(typeof result.count).toBe("number");
    expect(result.count).toBe(0);
    expect(Array.isArray(result.ids)).toBe(true);
    expect(result.ids).toHaveLength(0);
  });
}, 10_000);

// Error path: ErrProbeUnsupported cannot be triggered on linux (the OS probe
// is always available). See smoke-invariants.test.ts for the allow-list entry.
