/**
 * Smoke test — decide verb
 *
 * Happy path: allow an open permission request on a check_permission row with
 * relay_mode on; the result is the request's delivery facts (b.146 rule 15),
 * not_confirmed for a request no relay hook acks, with and without a
 * max_wait_ms bound. Error paths: decision "maybe" → ErrInvalidDecision,
 * refused before any store lookup (a placeholder token is enough); a negative
 * max_wait_ms → ErrInvalidFlags, nothing recorded.
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrInvalidDecision, ErrInvalidFlags } from "../../src/index.js";

/** Seeds a relay-on check_permission row id with one open request; returns the store and the request's token. */
function seedRequest(homeDir: string, id: string): { store: string; token: string } {
  const store = homeStore(homeDir);
  runHelper("seed-spawn", { store, state: "check_permission", id, "relay-mode": "on", "create-store": true });
  const { request_token } = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash" });
  return { store, token: request_token as string };
}

for (const [name, bound] of [
  ["no bound", undefined],
  ["max_wait_ms 2000", 2000],
] as const) {
  test(`decide: happy path — allows an open permission request, ${name}`, async () => {
    await withTempHome(async (homeDir) => {
      const id = "smoke-decide-id";
      const { store, token } = seedRequest(homeDir, id);
      using client = await openClient(store);
      const result = await client.decide({ claude_instance_id: id, request_token: token, decision: "allow", max_wait_ms: bound });
      expect(result).toMatchObject({
        delivery: "not_confirmed",
        hook_alive: null,
        hook_gone_at: null,
        attempted_decision: null,
        attempted_at: null,
        tool_use_id: null,
      });
      expect(Number.isNaN(Date.parse(result.confirm_by))).toBe(false);
    });
  }, 10_000);
}

test("decide: error — invalid decision string → ErrInvalidDecision", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const call = client.decide({
      claude_instance_id: "any-id",
      request_token: "00000000-0000-0000-0000-000000000000",
      decision: "maybe" as "allow",
    });
    await expect(call).rejects.toBeInstanceOf(ErrInvalidDecision);
  });
}, 10_000);

test("decide: error — negative max_wait_ms → ErrInvalidFlags, nothing recorded", async () => {
  await withTempHome(async (homeDir) => {
    const id = "smoke-decide-neg-id";
    const { store, token } = seedRequest(homeDir, id);
    using client = await openClient(store);
    await expect(client.decide({ claude_instance_id: id, request_token: token, decision: "allow", max_wait_ms: -1 }))
      .rejects.toBeInstanceOf(ErrInvalidFlags);
    expect((await client.getPermission({ request_token: token })).decision).toBeNull();
  });
}, 10_000);
