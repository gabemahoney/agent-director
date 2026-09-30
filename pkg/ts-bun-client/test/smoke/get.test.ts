/**
 * Smoke test — get verb
 *
 * Happy path: seed a working spawn, call get, assert required fields and no
 * launch_started_at; seed a pending spawn, assert an RFC3339 UTC
 * launch_started_at at the same instant as started_at (SR-20.3 default).
 * tmux_socket (AC-LKP-22): a row seeded with --socket shows that exact path;
 * a row seeded with --no-launch-identity (from before the release) has no key.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { GetResult } from "../../src/index.js";

const BOGUS_ID = "smoke-bogus-id-does-not-exist";
/** RFC3339 UTC as Go encodes a ms-precision time.Time: `Z`, 0-3 fraction digits. */
const RFC3339_UTC_MS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,3})?Z$/;

test("get: happy path — returns full spawn row fields", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-get-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: GetResult = await client.get({ claude_instance_id: spawnId });
    expect(result.claude_instance_id).toBe(spawnId);
    expect(typeof result.state).toBe("string");
    expect(typeof result.cwd).toBe("string");
    expect(typeof result.tmux_session_name).toBe("string");
    expect(typeof result.relay_mode).toBe("string");
    expect(typeof result.started_at).toBe("string");
    expect(typeof result.last_seen_at).toBe("string");
    expect(result.launch_started_at).toBeUndefined();
  });
}, 10_000);

test("get: pending row carries launch_started_at at its started_at instant (SR-22.2)", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-get-pending-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "pending",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: GetResult = await client.get({ claude_instance_id: spawnId });
    expect(result.state).toBe("pending");
    expect(typeof result.launch_started_at).toBe("string");
    expect(result.launch_started_at).toMatch(RFC3339_UTC_MS);
    const launchMs = Date.parse(result.launch_started_at as string);
    expect(Number.isNaN(launchMs)).toBe(false);
    expect(launchMs).toBe(Date.parse(result.started_at));
  });
}, 10_000);

test("get: row with a recorded socket shows that exact tmux_socket (AC-LKP-22)", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-get-socket-id";
    const socket = path.join(homeDir, "tmux-sock", "operator");

    runHelper("seed-spawn", {
      store: storePath,
      state: "ended",
      id: spawnId,
      socket,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: GetResult = await client.get({ claude_instance_id: spawnId });
    expect(result.tmux_socket).toBe(socket);
  });
}, 10_000);

test("get: row from before the release has no tmux_socket key (AC-LKP-22)", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-get-no-socket-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "ended",
      id: spawnId,
      "no-launch-identity": true,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result: GetResult = await client.get({ claude_instance_id: spawnId });
    expect(result.claude_instance_id).toBe(spawnId);
    expect(result.tmux_socket).toBeUndefined();
    expect(Object.keys(result)).not.toContain("tmux_socket");
  });
}, 10_000);

test("get: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

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
