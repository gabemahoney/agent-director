/**
 * Smoke test — expire verb
 *
 * Every row is seeded ended on a private socket, so expire looks each up there
 * (SR-12.2): one with no session in the fake's table is Gone and deleted (in
 * `ids`); one whose own labelled session is in the table (seed-row-session) is
 * Ours and kept (in `kept_ids`). Error path: none; expire declares no
 * ErrorNames (smoke-invariants allow-list).
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, homeStore, openClient } from "../internal/helper.js";

/** Seeds an ended row with each id on the home's private socket, then expires every finished row. */
async function seedAndExpire(homeDir: string, ids: string[], keep: string[] = []) {
  const store = homeStore(homeDir);
  for (const id of ids) runHelper("seed-spawn", { store, state: "ended", id, "create-store": true, socket: privateTmuxSocket(homeDir) });
  for (const id of keep) runHelper("seed-row-session", { store, id });
  using client = await openClient(store);
  return await client.expire({ older_than: "0d" });
}

test("expire: happy path — an ended row with no session is deleted, none kept", async () => {
  await withTempHome(async (homeDir) => {
    expect(await seedAndExpire(homeDir, ["smoke-expire-id"])).toEqual({ count: 1, ids: ["smoke-expire-id"], kept: 0, kept_ids: [] });
  });
}, 10_000);

test("expire: an ended row whose own session is still in tmux is kept, not deleted", async () => {
  await withTempHome(async (homeDir) => {
    const result = await seedAndExpire(homeDir, ["smoke-expire-gone-id", "smoke-expire-kept-id"], ["smoke-expire-kept-id"]);
    expect(result).toEqual({ count: 1, ids: ["smoke-expire-gone-id"], kept: 1, kept_ids: ["smoke-expire-kept-id"] });
  });
}, 10_000);
