/**
 * Smoke test — get verb
 *
 * Happy path: seed a working spawn, call get, assert required fields.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { GetResult } from "../../src/index.js";

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("get: happy path — returns full spawn row fields", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    const spawnId = "smoke-get-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true });
    const result: GetResult = await client.get({ claude_instance_id: spawnId });
    expect(result.claude_instance_id).toBe(spawnId);
    expect(typeof result.state).toBe("string");
    expect(typeof result.cwd).toBe("string");
    expect(typeof result.tmux_session_name).toBe("string");
    expect(typeof result.relay_mode).toBe("string");
    expect(typeof result.started_at).toBe("string");
    expect(typeof result.last_seen_at).toBe("string");
  });
}, 10_000);

test("get: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    let caught: unknown;
    try {
      await client.get({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
