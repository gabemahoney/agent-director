/**
 * Smoke test — get-permission verb
 *
 * Happy path: an open request on a check_permission row is returned by its
 * request_token, undecided, with its delivery facts (b.146 rule 15). Error
 * path: unknown token → ErrPermissionRequestNotFound.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrPermissionRequestNotFound } from "../../src/index.js";

test("get-permission: happy path — returns open permission request row", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    runHelper("seed-spawn", { store, state: "check_permission", id: "smoke-gp-id", "relay-mode": "on", "create-store": true });
    const token = runHelper("seed-permission-request", { store, "spawn-id": "smoke-gp-id", tool: "Bash" })["request_token"] as string;
    using client = await openClient(store);
    const result = await client.getPermission({ request_token: token });
    expect(result).toMatchObject({ request_token: token, tool_name: "Bash" });
    expect([result.decision ?? null, result.decision_reason ?? null, result.decided_at ?? null]).toEqual([null, null, null]);
    expect(result.requested_at.length).toBeGreaterThan(0);
    // b.146 rule 15: the delivery facts, a request no relay hook acks reading not_confirmed.
    expect(result).toMatchObject({ delivery: "not_confirmed", hook_alive: null, hook_gone_at: null,
      attempted_decision: null, attempted_at: null, tool_use_id: null });
    expect(Number.isNaN(Date.parse(result.confirm_by))).toBe(false);
  });
}, 10_000);

test("get-permission: error — unknown token → ErrPermissionRequestNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.getPermission({ request_token: "00000000-0000-0000-0000-000000000000" }))
      .rejects.toBeInstanceOf(ErrPermissionRequestNotFound);
  });
}, 10_000);
