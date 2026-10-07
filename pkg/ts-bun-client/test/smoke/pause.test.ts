/**
 * Smoke test — pause verb
 *
 * Happy path: an ended row is already terminal, so pause is a no-op success
 * with no tmux call. Error path: a working row → ErrSpawnNotPausable.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotPausable } from "../../src/index.js";

test("pause: happy path — no-op for already-terminal spawn", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "ended", id: "smoke-pause-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    expect(await client.pause({ claude_instance_id: "smoke-pause-id" })).toEqual({});
  });
}, 10_000);

test("pause: error — working spawn is not pausable → ErrSpawnNotPausable", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "working", id: "smoke-pause-err-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    await expect(client.pause({ claude_instance_id: "smoke-pause-err-id" })).rejects.toBeInstanceOf(ErrSpawnNotPausable);
  });
}, 10_000);
