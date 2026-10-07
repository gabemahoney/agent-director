/**
 * Smoke test — read-pane verb
 *
 * Happy path: a working row on a private socket whose own labelled session is
 * in the fake-tmux table (seed-row-session) with a known capture: read-pane
 * finds it Ours and returns exactly that text (SR-7.2, SR-3.7).
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

test("read-pane: happy path — returns the row's pane text from its fake-tmux session", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-read-pane-id";
    const capture = "smoke read-pane line one\nsmoke read-pane line two\n";
    runHelper("seed-spawn", { store, state: "working", id, "create-store": true, socket: privateTmuxSocket(homeDir) });
    runHelper("seed-row-session", { store, id, capture });
    using client = await openClient(store);
    expect((await client.readPane({ claude_instance_id: id, n_lines: 5 })).pane).toBe(capture);
  });
}, 10_000);

test("read-pane: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.readPane({ claude_instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
