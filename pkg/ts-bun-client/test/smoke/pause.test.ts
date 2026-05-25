/**
 * Smoke test — pause verb
 *
 * Happy path: seed a spawn in ended state. pause short-circuits to no-op
 * success when the row is already terminal (ended/missing) — no tmux
 * interaction, no polling.
 *
 * Error path: seed a spawn in working state → ErrSpawnNotPausable (state ∈
 * {pending, working, ask_user, check_permission} is not pausable via /exit).
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotPausable, AgentDirectorError } from "../../src/index.js";

test("pause: happy path — no-op for already-terminal spawn", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-pause-id";

    // Seed in ended state — pause short-circuits before any tmux call.
    runHelper("seed-spawn", {
      store: storePath,
      state: "ended",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true });
    // PauseResult is an empty object; just assert no throw.
    const result = await client.pause({ claude_instance_id: spawnId });
    expect(typeof result).toBe("object");
  });
}, 10_000);

test("pause: error — working spawn is not pausable → ErrSpawnNotPausable", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-pause-err-id";

    // Seed in working state — pause rejects this state with ErrSpawnNotPausable.
    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
    });

    using client = new Client({ storePath, createIfMissing: true });
    let caught: unknown;
    try {
      await client.pause({ claude_instance_id: spawnId });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotPausable);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
