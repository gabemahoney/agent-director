/**
 * Smoke test — send-keys verb
 *
 * Happy path: seed a waiting spawn on a private socket, then write the row's
 * own labelled session into the fake-tmux table (ts-helper seed-row-session);
 * send-keys finds it Ours and sends the text, then Enter, to the row's pane by
 * id (SR-7.2, SR-3.7). The fake's argv log shows both sends.
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("send-keys: happy path — sends the text, then Enter, to the row's pane by id", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-send-keys-id";
    const logPath = path.join(homeDir, "fake-tmux.log");

    runHelper("seed-spawn", {
      store: storePath,
      state: "waiting",
      id: spawnId,
      "create-store": true,
      socket: privateTmuxSocket(homeDir),
    });
    const seeded = runHelper("seed-row-session", { store: storePath, id: spawnId });
    const paneId = seeded["pane_id"] as string;

    // The client's CLI subprocess inherits process.env on each call.
    const priorLog = process.env.FAKE_TMUX_LOG;
    process.env.FAKE_TMUX_LOG = logPath;
    try {
      using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin , _cliPath: process.env.CLI_PATH } as any);
      const result = await client.sendKeys({
        claude_instance_id: spawnId,
        text: "hello smoke",
      });
      // SendKeysResult is {} — an object, not an error envelope.
      expect(typeof result).toBe("object");
    } finally {
      if (priorLog !== undefined) process.env.FAKE_TMUX_LOG = priorLog;
      else delete process.env.FAKE_TMUX_LOG;
    }

    const sends = fs
      .readFileSync(logPath, "utf8")
      .split("---\n")
      .map((rec) => rec.split("\n").slice(0, -1))
      .filter((argv) => argv.includes("send-keys"))
      .map((argv) => argv.slice(argv.indexOf("send-keys")));
    expect(sends).toEqual([
      ["send-keys", "-t", paneId, "-l", "--", "hello smoke"],
      ["send-keys", "-t", paneId, "Enter"],
    ]);
  });
}, 10_000);

test("send-keys: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

    let caught: unknown;
    try {
      await client.sendKeys({ claude_instance_id: BOGUS_ID, text: "hello" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
