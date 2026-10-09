/**
 * Smoke test — get verb
 *
 * Happy path: a working row's fields, with no launch_started_at; a pending
 * row's launch_started_at, RFC3339 UTC at its started_at instant (SR-22.2).
 * tmux_socket (AC-LKP-22): a row seeded with --socket shows that exact path; a
 * row from before the release (--no-launch-identity) has no key. A
 * check_permission row lists its open requests with their delivery facts.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, homeStore, openClient } from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";
import type { GetResult } from "../../src/index.js";

/** RFC3339 UTC as Go encodes a ms-precision time.Time: `Z`, 0-3 fraction digits. */
const RFC3339_UTC_MS = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,3})?Z$/;

/** Seeds one row (seed-spawn flags) in a temp HOME's store and returns get() on it. */
async function seedAndGet(homeDir: string, id: string, flags: Record<string, string | true>): Promise<GetResult> {
  runHelper("seed-spawn", { store: homeStore(homeDir), id, "create-store": true, ...flags });
  using client = await openClient(homeStore(homeDir));
  return await client.get({ claude_instance_id: id });
}

test("get: happy path — returns full spawn row fields", async () => {
  await withTempHome(async (homeDir) => {
    const result = await seedAndGet(homeDir, "smoke-get-id", { state: "working" });
    expect(result).toMatchObject({ claude_instance_id: "smoke-get-id", state: "working" });
    for (const field of ["cwd", "tmux_session_name", "relay_mode", "started_at", "last_seen_at"] as const) {
      expect(typeof result[field]).toBe("string");
    }
    expect(result.launch_started_at).toBeUndefined();
  });
}, 10_000);

test("get: pending row carries launch_started_at at its started_at instant (SR-22.2)", async () => {
  await withTempHome(async (homeDir) => {
    const result = await seedAndGet(homeDir, "smoke-get-pending-id", { state: "pending" });
    expect(result.state).toBe("pending");
    expect(result.launch_started_at).toMatch(RFC3339_UTC_MS);
    expect(Date.parse(result.launch_started_at!)).toBe(Date.parse(result.started_at));
  });
}, 10_000);

test("get: row with a recorded socket shows that exact tmux_socket (AC-LKP-22)", async () => {
  await withTempHome(async (homeDir) => {
    const socket = path.join(homeDir, "tmux-sock", "operator");
    expect((await seedAndGet(homeDir, "smoke-get-socket-id", { state: "ended", socket })).tmux_socket).toBe(socket);
  });
}, 10_000);

test("get: row from before the release has no tmux_socket key (AC-LKP-22)", async () => {
  await withTempHome(async (homeDir) => {
    const result = await seedAndGet(homeDir, "smoke-get-no-socket-id", { state: "ended", "no-launch-identity": true });
    expect(result.claude_instance_id).toBe("smoke-get-no-socket-id");
    expect(Object.keys(result)).not.toContain("tmux_socket");
  });
}, 10_000);

test("get: a check_permission row carries its open requests with their delivery facts (b.146 rule 15)", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    runHelper("seed-spawn", { store, state: "check_permission", id: "smoke-get-cp-id", "relay-mode": "on", "create-store": true });
    const token = runHelper("seed-permission-request", { store, "spawn-id": "smoke-get-cp-id", tool: "Bash" })["request_token"];
    using client = await openClient(store);
    const result = await client.get({ claude_instance_id: "smoke-get-cp-id" });
    expect(result.permission_requests).toHaveLength(1);
    expect(result.permission_requests[0]).toMatchObject({ request_token: token, tool_name: "Bash", decision: null,
      decision_reason: null, delivery: "not_confirmed", hook_alive: null, hook_gone_at: null, attempted_decision: null,
      attempted_at: null, tool_use_id: null });
    expect(Number.isNaN(Date.parse(result.permission_requests[0]!.confirm_by))).toBe(false);
  });
}, 10_000);

test("get: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.get({ claude_instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
