/**
 * Smoke test — decide verb
 *
 * Happy path: allow an open permission request on a check_permission row with
 * relay_mode on. Error path: decision "maybe" → ErrInvalidDecision, refused
 * before any store lookup (a placeholder token is enough).
 */

import { test, expect } from "bun:test";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrInvalidDecision } from "../../src/index.js";

test("decide: happy path — allows an open permission request", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-decide-id";
    runHelper("seed-spawn", { store, state: "check_permission", id, "relay-mode": "on", "create-store": true });
    const { request_token } = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash" });
    using client = await openClient(store);
    expect(await client.decide({ claude_instance_id: id, request_token: request_token as string, decision: "allow" })).toEqual({});
  });
}, 10_000);

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
