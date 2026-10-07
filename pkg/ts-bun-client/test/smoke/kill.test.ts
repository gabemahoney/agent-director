/**
 * Smoke test — kill verb
 *
 * Happy path: a working row on a private socket with no fake-tmux table looks
 * Gone and its recorded pane pid is never live, so kill succeeds with
 * kill_sent false (SR-6.1, SR-6.6). Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

test("kill: happy path — Gone row, no kill sent", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    runHelper("seed-spawn", { store, state: "working", id: "smoke-kill-id", "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(store);
    expect(await client.kill({ claude_instance_id: "smoke-kill-id" })).toEqual({ kill_sent: false });
  });
}, 10_000);

test("kill: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.kill({ claude_instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
