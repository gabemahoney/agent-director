/**
 * Smoke test — find-missing verb
 *
 * Happy path: an empty store has nothing to judge, so count 0 and no ids.
 * Error path: none; the manifest still lists ErrProbeUnsupported (SR-1.7) but
 * find-missing no longer returns it (smoke-invariants allow-list).
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { homeStore, openClient } from "../internal/helper.js";

test("find-missing: happy path — empty store returns count=0", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    expect(await client.findMissing({})).toMatchObject({ count: 0, ids: [] });
  });
}, 10_000);
