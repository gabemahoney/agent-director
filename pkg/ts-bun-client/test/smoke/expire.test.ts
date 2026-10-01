/**
 * Smoke test — expire verb
 *
 * Every row is seeded `ended` on a private socket and the client runs tmux as
 * the fake-tmux binary, so no host tmux is reached. A seeded ended row records
 * no agent process, so expire looks it up on its socket (SR-12.2):
 *   - deleted: the fake's table holds no session for the row → Gone → the row
 *     is deleted and its id is in `ids`;
 *   - kept: the row's own labelled session is in the fake's table
 *     (ts-helper seed-row-session) → Ours → the id is in `kept_ids`, not `ids`.
 *
 * Error path: expire declares no ErrorNames in the manifest. This verb is in
 * the smoke-invariants allow-list for missing error-case tests.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket } from "../internal/helper.js";
import { Client } from "../../src/index.js";
import type { ExpireResult } from "../../src/index.js";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

/** Seeds an ended row with the given id on the home's private socket. */
function seedEnded(storePath: string, homeDir: string, id: string): void {
  runHelper("seed-spawn", {
    store: storePath,
    state: "ended",
    id,
    "create-store": true,
    socket: privateTmuxSocket(homeDir),
  });
}

/** Runs expire over every finished row (older_than="0d") through the fake tmux. */
async function expireAll(storePath: string): Promise<ExpireResult> {
  using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin, _cliPath: process.env.CLI_PATH } as any);
  return await client.expire({ older_than: "0d" });
}

test("expire: happy path — an ended row with no session is deleted, none kept", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-expire-id";
    seedEnded(storePath, homeDir, spawnId);

    const result = await expireAll(storePath);
    expect(result).toEqual({ count: 1, ids: [spawnId], kept: 0, kept_ids: [] });
  });
}, 10_000);

test("expire: an ended row whose own session is still in tmux is kept, not deleted", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const goneId = "smoke-expire-gone-id";
    const keptId = "smoke-expire-kept-id";
    seedEnded(storePath, homeDir, goneId);
    seedEnded(storePath, homeDir, keptId);
    runHelper("seed-row-session", { store: storePath, id: keptId });

    const result = await expireAll(storePath);
    expect(result).toEqual({ count: 1, ids: [goneId], kept: 1, kept_ids: [keptId] });
  });
}, 10_000);

// Error path: expire declares no ErrorNames in the manifest.
// No error-case test is included. See smoke-invariants.test.ts for the
// allow-list entry that exempts this verb.
