/**
 * Smoke test — kill verb
 *
 * Happy path: seed a working spawn on a private socket with no fake-tmux
 * table; the lookup answers Gone and the recorded pane pid is never a live
 * process, so kill succeeds with kill_sent false (SR-6.1, SR-6.6).
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("kill: happy path — Gone row, no kill sent", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-kill-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
      socket: privateTmuxSocket(homeDir),
    });

    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    const result = await client.kill({ claude_instance_id: spawnId });
    expect(typeof result.kill_sent).toBe("boolean");
    expect(result).toEqual({ kill_sent: false });
  });
}, 10_000);

test("kill: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      await client.kill({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
