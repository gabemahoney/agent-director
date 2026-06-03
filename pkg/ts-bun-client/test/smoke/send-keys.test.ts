/**
 * Smoke test — send-keys verb
 *
 * Happy path: seed a waiting (interactive) spawn, call sendKeys. The
 * fake-tmux stub on PATH captures the tmux send-keys call and exits 0.
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

test("send-keys: happy path — delivers text to waiting spawn", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-send-keys-id";

    runHelper("seed-spawn", {
      store: storePath,
      state: "waiting",
      id: spawnId,
      "create-store": true,
    });

    using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin , _cliPath: process.env.CLI_PATH } as any);
    // SendKeysResult is an empty object; just assert no throw.
    const result = await client.sendKeys({
      claude_instance_id: spawnId,
      text: "hello smoke",
    });
    // Result is {} — verify it's an object (not an error envelope).
    expect(typeof result).toBe("object");
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
