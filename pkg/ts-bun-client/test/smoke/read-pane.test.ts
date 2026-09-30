/**
 * Smoke test — read-pane verb
 *
 * Happy path: seed a working spawn on a private socket, then write the row's
 * own labelled session into the fake-tmux table (ts-helper seed-row-session)
 * with a known capture text; read-pane finds it Ours, captures the row's pane
 * by id and returns exactly that text (SR-7.2, SR-3.7).
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { ReadPaneResult } from "../../src/index.js";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("read-pane: happy path — returns the row's pane text from its fake-tmux session", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-read-pane-id";
    const paneText = "smoke read-pane line one\nsmoke read-pane line two\n";

    runHelper("seed-spawn", {
      store: storePath,
      state: "working",
      id: spawnId,
      "create-store": true,
      socket: privateTmuxSocket(homeDir),
    });
    runHelper("seed-row-session", { store: storePath, id: spawnId, capture: paneText });

    using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin , _cliPath: process.env.CLI_PATH } as any);
    const result: ReadPaneResult = await client.readPane({
      claude_instance_id: spawnId,
      n_lines: 5,
    });
    expect(result.pane).toBe(paneText);
  });
}, 10_000);

test("read-pane: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      await client.readPane({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
