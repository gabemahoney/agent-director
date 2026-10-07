/**
 * Smoke test — status verb
 *
 * Happy path: a working row's state, with no launch_started_at; a pending row's
 * launch_started_at as RFC3339 UTC (SR-22.2). Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

/** RFC3339 UTC as Go encodes a ms-precision time.Time: `Z`, 0-3 fraction digits. */
const RFC3339_UTC_MS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,3})?Z$/;

test("status: happy path — returns state field for seeded spawn", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "working", id: "smoke-status-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    const result = await client.status({ claude_instance_id: "smoke-status-id" });
    expect(result.state).toBe("working");
    expect(result.launch_started_at).toBeUndefined();
  });
}, 10_000);

test("status: pending row carries launch_started_at as RFC3339 UTC (SR-22.2)", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "pending", id: "smoke-status-pending-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    const result = await client.status({ claude_instance_id: "smoke-status-pending-id" });
    expect(result.state).toBe("pending");
    expect(result.launch_started_at).toMatch(RFC3339_UTC_MS);
  });
}, 10_000);

test("status: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.status({ claude_instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
