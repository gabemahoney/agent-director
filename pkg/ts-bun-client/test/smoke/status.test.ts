/**
 * Smoke test — status verb
 *
 * Happy path: seed a working spawn, call status, assert state field and no
 * launch_started_at; seed a pending spawn, assert an RFC3339 UTC launch_started_at.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { StatusResult } from "../../src/index.js";

const BOGUS_ID = "smoke-bogus-id-does-not-exist";
/** RFC3339 UTC as Go encodes a ms-precision time.Time: `Z`, 0-3 fraction digits. */
const RFC3339_UTC_MS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,3})?Z$/;

test("status: happy path — returns state field for seeded spawn", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-status-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: StatusResult = await client.status({ claude_instance_id: spawnId });
    expect(typeof result.state).toBe("string");
    expect(result.state.length).toBeGreaterThan(0);
    expect(result.launch_started_at).toBeUndefined();
  });
}, 10_000);

test("status: pending row carries launch_started_at as RFC3339 UTC (SR-22.2)", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-status-pending-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "pending",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: StatusResult = await client.status({ claude_instance_id: spawnId });
    expect(result.state).toBe("pending");
    expect(typeof result.launch_started_at).toBe("string");
    expect(result.launch_started_at).toMatch(RFC3339_UTC_MS);
  });
}, 10_000);

test("status: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      await client.status({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
