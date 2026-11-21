/**
 * Smoke test — force-delivery verb
 *
 * Happy path (SR-4.1, SR-9.2): busy send doesn't cover codex, so forcing a
 * codex row succeeds with a refused attempt, reason agent_not_covered, under
 * a new force_id, which getForce then reads back as found.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

test("force-delivery: happy path — a refused attempt that getForce finds", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "waiting", id: "smoke-force-id", agent: "codex", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    const { force_result } = await client.forceDelivery({ instance_id: "smoke-force-id" });
    expect(force_result).toMatchObject({ outcome: "refused", reason: "agent_not_covered", carried_pairs: [], carried_results: [] });
    expect(force_result.force_id).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/);
    expect(await client.getForce({ force_id: force_result.force_id })).toEqual({ lookup: "found", force_result });
  });
}, 10_000);

test("force-delivery: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.forceDelivery({ instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
