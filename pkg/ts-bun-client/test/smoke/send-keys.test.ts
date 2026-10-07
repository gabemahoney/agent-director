/**
 * Smoke test — send-keys verb
 *
 * Happy path: a waiting row on a private socket whose own labelled session is
 * in the fake-tmux table: send-keys finds it Ours and sends the text, then
 * Enter, to the row's pane by id (SR-7.2, SR-3.7), as the fake's log shows.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, fakeTmuxCalls, withProcessEnv, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

test("send-keys: happy path — sends the text, then Enter, to the row's pane by id", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-send-keys-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    runHelper("seed-spawn", { store, state: "waiting", id, "create-store": true, socket: privateTmuxSocket(homeDir) });
    const paneId = runHelper("seed-row-session", { store, id })["pane_id"] as string;

    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      expect(await client.sendKeys({ claude_instance_id: id, text: "hello smoke" })).toEqual({});
    });
    const sends = fakeTmuxCalls(logPath)
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
    using client = await openClient(homeStore(homeDir));
    await expect(client.sendKeys({ claude_instance_id: "smoke-bogus-id", text: "hello" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
