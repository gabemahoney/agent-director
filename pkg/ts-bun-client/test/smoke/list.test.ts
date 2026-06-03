/**
 * Smoke test — list verb
 *
 * Happy path: seed a working spawn, call list with no filters, assert spawns
 * array is non-empty and each row has the required fields.
 *
 * Error path: label with missing "=" separator → ErrListInvalidLabel.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrListInvalidLabel, AgentDirectorError } from "../../src/index.js";
import type { ListResult } from "../../src/index.js";

test("list: happy path — returns seeded spawns array", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-list-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: ListResult = await client.list({});
    expect(Array.isArray(result.spawns)).toBe(true);
    expect(result.spawns.length).toBeGreaterThanOrEqual(1);
    const row = result.spawns.find((r) => r.claude_instance_id === spawnId);
    expect(row).toBeDefined();
    expect(typeof row!.state).toBe("string");
    expect(typeof row!.cwd).toBe("string");
  });
}, 10_000);

test("list: error — invalid label format → ErrListInvalidLabel", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      // Label without "=" is invalid syntax.
      await client.list({ label: ["no-equals-sign"] });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrListInvalidLabel);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
