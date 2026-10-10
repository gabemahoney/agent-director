/**
 * Smoke test — record-pane-answer verb (b.146 rule 13)
 *
 * Happy path: a request that fell back by its relay window, its hook gone a
 * minute ago, on a row whose own session is in the fake-tmux table: readPane's
 * pane_sha256 recorded with as "unknown" closes it (pane_answer outside,
 * decision null), typing nothing. Error paths: a request still in its relay
 * window → ErrClaimTooSoon whose errDetails carry not_before, its confirm_by
 * plus 2 s; an unknown token → ErrPermissionRequestNotFound with errDetails
 * null.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, fakeTmuxCalls, withProcessEnv, homeStore, openClient, rejection } from "../internal/helper.js";
import { ErrClaimTooSoon, ErrPermissionRequestNotFound, type AgentDirectorError, type ClaimTooSoonDetails } from "../../src/index.js";

const ZERO_HASH = "0".repeat(64);

test("record-pane-answer: happy path — closes a fallen-back request, typing nothing", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-rpa-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    runHelper("seed-spawn", { store, state: "check_permission", id, "relay-mode": "on", "create-store": true,
      socket: privateTmuxSocket(homeDir) });
    const token = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash",
      "created-ago-seconds": "172800", "hook-gone-ago-seconds": "60" })["request_token"] as string;
    runHelper("seed-row-session", { store, id, capture: "answered at tmux\n" });

    const result = await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      const { pane_sha256 } = await client.readPane({ claude_instance_id: id });
      return await client.recordPaneAnswer({ request_token: token, as: "unknown", expect_pane_sha256: pane_sha256 });
    });

    expect(result).toEqual({ request_token: token, pane_answer: "outside", pane_as: "unknown", decision: null,
      decision_reason: "pane_outside" });
    expect(fakeTmuxCalls(logPath).filter((argv) => argv.includes("send-keys"))).toEqual([]);
  });
}, 15_000);

test("record-pane-answer: error — too soon → ErrClaimTooSoon with errDetails.not_before confirm_by + 2 s", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    runHelper("seed-spawn", { store, state: "check_permission", id: "smoke-rpa-soon", "relay-mode": "on", "create-store": true });
    const token = runHelper("seed-permission-request", { store, "spawn-id": "smoke-rpa-soon", tool: "Bash" })["request_token"] as string;
    using client = await openClient(store);
    const { confirm_by } = await client.getPermission({ request_token: token });

    const err = (await rejection(client.recordPaneAnswer({ request_token: token, as: "allow", expect_pane_sha256: ZERO_HASH }))) as AgentDirectorError;

    // Inside its relay window, its relay hook (recorded before schema v7) cannot be checked.
    expect(err).toBeInstanceOf(ErrClaimTooSoon);
    const details = err.errDetails as unknown as ClaimTooSoonDetails;
    expect({ ...details, not_before: "" }).toEqual({ request_token: token, hook_alive: null, hook_gone_at: null, not_before: "" });
    expect(Date.parse(details.not_before ?? "") - Date.parse(confirm_by)).toBe(2000);
  });
}, 10_000);

test("record-pane-answer: error — unknown token → ErrPermissionRequestNotFound, no errDetails", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    const err = (await rejection(client.recordPaneAnswer({ request_token: "00000000-0000-0000-0000-000000000000",
      as: "deny", expect_pane_sha256: ZERO_HASH }))) as AgentDirectorError;
    expect(err).toBeInstanceOf(ErrPermissionRequestNotFound);
    expect(err.errDetails).toBeNull();
  });
}, 10_000);
