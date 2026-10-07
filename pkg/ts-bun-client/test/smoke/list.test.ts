/**
 * Smoke test — list verb
 *
 * Happy path: the seeded row is listed with its state and cwd.
 * Error path: a label without "=" → ErrListInvalidLabel.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrListInvalidLabel } from "../../src/index.js";

test("list: happy path — returns seeded spawns array", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "working", id: "smoke-list-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    const row = (await client.list({})).spawns.find((r) => r.claude_instance_id === "smoke-list-id");
    expect(row?.state).toBe("working");
    expect(typeof row?.cwd).toBe("string");
  });
}, 10_000);

test("list: error — invalid label format → ErrListInvalidLabel", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.list({ label: ["no-equals-sign"] })).rejects.toBeInstanceOf(ErrListInvalidLabel);
  });
}, 10_000);
