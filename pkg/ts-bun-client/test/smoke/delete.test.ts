/**
 * Smoke test — delete verb
 *
 * Happy path: seed a spawn, call delete with its id, assert results["id"]="ok".
 *
 * Documented error behavior: delete has no verb-level ErrorNames in the
 * manifest; per-row errors surface in the results map (not as thrown errors).
 * The "documented error" test below verifies that an unknown id records
 * "ErrSpawnNotFound" in the results map rather than throwing.
 *
 * Because no exception is thrown, delete is in the smoke-invariants allow-list
 * for the instanceof-Err grep check.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client } from "../../src/index.js";
import type { DeleteResult } from "../../src/index.js";

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("delete: happy path — removes a seeded spawn row", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    const spawnId = "smoke-delete-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true });
    const result: DeleteResult = await client.delete({
      claude_instance_id: [spawnId],
    });
    expect(typeof result.results).toBe("object");
    expect(result.results[spawnId]).toBe("ok");
  });
}, 10_000);

test("delete: documented error behavior — unknown id records ErrSpawnNotFound in results map", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    // delete never throws at the verb level; per-row errors are in the map.
    const result: DeleteResult = await client.delete({
      claude_instance_id: [BOGUS_ID],
    });
    expect(result.results[BOGUS_ID]).toBe("ErrSpawnNotFound");
  });
}, 10_000);
