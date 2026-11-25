/**
 * Smoke test — list verb
 *
 * Happy path: the seeded row is listed with its state, cwd and agent (SR-2.1), and a
 * check_permission row with its open requests and their delivery facts.
 * Error path: a label without "=" → ErrListInvalidLabel.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrListInvalidLabel } from "../../src/index.js";

test("list: happy path — returns seeded spawns array", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "working", "writer-pid": String(process.pid), id: "smoke-list-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    const row = (await client.list({})).spawns.find((r) => r.instance_id === "smoke-list-id");
    expect(row?.state).toBe("working");
    expect(typeof row?.cwd).toBe("string");
    expect(row?.permission_requests).toEqual([]);
    expect(row?.agent).toBe("claude");
    // b.cqr: the CLI preserves an explicit null for an unsupported agent.
    expect(row?.last_turn_errored).toBeNull();
  });
}, 10_000);

// b.cqr: the selected Codex row's error observation survives the real CLI envelope.
test("list: an errored Codex row carries last_turn_errored true", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "waiting", id: "smoke-list-codex-id",
      "codex-errored-turn": true, "writer-pid": String(process.pid), "create-store": true });
    using client = await openClient(homeStore(homeDir));
    expect((await client.list({})).spawns.find((r) => r.instance_id === "smoke-list-codex-id"))
      .toMatchObject({ agent: "codex", state: "waiting", last_turn_errored: true });
  });
}, 10_000);

test("list: a check_permission row carries its open requests with their delivery facts (b.146 rule 15)", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    runHelper("seed-spawn", { store, state: "check_permission", "writer-pid": String(process.pid), id: "smoke-list-cp-id", "relay-mode": "on", "create-store": true });
    const token = runHelper("seed-permission-request", { store, "spawn-id": "smoke-list-cp-id", tool: "Bash" })["request_token"];
    using client = await openClient(store);
    const row = (await client.list({})).spawns.find((r) => r.instance_id === "smoke-list-cp-id");
    expect(row?.permission_requests).toHaveLength(1);
    expect(row?.permission_requests[0]).toMatchObject({ request_token: token, decision: null, delivery: "not_confirmed",
      hook_alive: null, hook_gone_at: null, attempted_decision: null, attempted_at: null, tool_use_id: null });
  });
}, 10_000);

test("list: error — invalid label format → ErrListInvalidLabel", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.list({ label: ["no-equals-sign"] })).rejects.toBeInstanceOf(ErrListInvalidLabel);
  });
}, 10_000);
