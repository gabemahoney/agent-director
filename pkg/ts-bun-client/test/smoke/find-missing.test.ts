/**
 * Smoke test — find-missing verb
 *
 * Happy path: call on an empty store. No spawns exist, so count=0 and ids=[].
 * The verb judges each live row by its agent process; on an empty store
 * there is nothing to judge.
 *
 * Error path: none. The manifest still lists ErrProbeUnsupported for
 * find-missing (SR-1.7), but find-missing no longer returns it. This verb is
 * in the smoke-invariants allow-list for missing error-case tests.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { Client } from "../../src/index.js";
import type { FindMissingResult } from "../../src/index.js";

test("find-missing: happy path — empty store returns count=0", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: FindMissingResult = await client.findMissing({});
    expect(typeof result.count).toBe("number");
    expect(result.count).toBe(0);
    expect(Array.isArray(result.ids)).toBe(true);
    expect(result.ids).toHaveLength(0);
  });
}, 10_000);

// Error path: none; find-missing no longer returns ErrProbeUnsupported (kept by
// SR-1.7). See smoke-invariants.test.ts for the allow-list entry.
