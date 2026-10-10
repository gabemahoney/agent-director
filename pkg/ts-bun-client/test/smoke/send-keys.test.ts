/**
 * Smoke test — send-keys verb
 *
 * Happy path: a waiting row on a private socket whose own labelled session is
 * in the fake-tmux table: send-keys finds it Ours and sends the text, then
 * Enter, to the row's pane by id (SR-7.2, SR-3.7), as the fake's log shows.
 * Error paths: a pane answer (b.146 rule 8) whose sent write fails after its
 * one key → ErrInternal whose errDetails say key_sent; a plain call while a
 * request is not proven gone (b.146 step 2c) → ErrDialogMaybeOpen, which the
 * pane's hash passes; unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import { Database } from "bun:sqlite";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, fakeTmuxCalls, withProcessEnv, homeStore, openClient, rejection } from "../internal/helper.js";
import { ErrDialogMaybeOpen, ErrInternal, ErrSpawnNotFound, type AgentDirectorError, type DialogMaybeOpenDetails,
  type PaneKeySentDetails } from "../../src/index.js";

test("send-keys: happy path — sends the text, then Enter, to the row's pane by id", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-send-keys-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    runHelper("seed-spawn", { store, state: "waiting", id, "create-store": true, socket: privateTmuxSocket(homeDir) });
    const paneId = runHelper("seed-row-session", { store, id })["pane_id"] as string;

    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      expect(await client.sendKeys({ claude_instance_id: id, text: "hello smoke" })).toEqual({});
    });
    const sends = fakeTmuxCalls(logPath)
      .filter((argv) => argv.includes("send-keys"))
      .map((argv) => argv.slice(argv.indexOf("send-keys")));
    expect(sends).toEqual([
      ["send-keys", "-t", paneId, "-l", "--", "hello smoke"],
      ["send-keys", "-t", paneId, "Enter"],
    ]);
  });
}, 10_000);

test("send-keys: error — a pane answer whose sent write fails after its key → ErrInternal, errDetails key_sent", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-send-keys-answer-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    runHelper("seed-spawn", { store, state: "check_permission", id, "relay-mode": "on", "create-store": true,
      socket: privateTmuxSocket(homeDir) });
    // Past its relay window: fallen back.
    const token = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash",
      "created-ago-seconds": "172800" })["request_token"] as string;
    const paneId = runHelper("seed-row-session", { store, id, capture: "Allow Bash? 1 yes, 2 no\n" })["pane_id"] as string;
    // The sent write records the pane answer's decision once its key went out: make it fail.
    const db = new Database(store);
    db.run(`CREATE TRIGGER smoke_fail_pane_sent BEFORE UPDATE OF decision ON permission_requests
      WHEN NEW.pane_answer = 'sent' BEGIN SELECT RAISE(ABORT, 'injected: the sent write fails'); END`);
    db.close();

    const err = await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      const { pane_sha256 } = await client.readPane({ claude_instance_id: id });
      return (await rejection(client.sendKeys({ claude_instance_id: id, request_token: token, as: "allow", key: "1",
        expect_pane_sha256: pane_sha256 }))) as AgentDirectorError;
    });

    expect(err).toBeInstanceOf(ErrInternal);
    const details: PaneKeySentDetails = { key_sent: true, request_token: token };
    expect(err.errDetails).toEqual(details as unknown as Record<string, unknown>);
    const sends = fakeTmuxCalls(logPath)
      .filter((argv) => argv.includes("send-keys"))
      .map((argv) => argv.slice(argv.indexOf("send-keys")));
    expect(sends).toEqual([["send-keys", "-t", paneId, "-l", "--", "1"]]);
  });
}, 10_000);

// b.146 step 2c: recordPaneAnswer closes the request in agent-director's records but proves nothing.
test("send-keys: error — a request not proven gone → ErrDialogMaybeOpen with errDetails; the pane's hash passes the hold", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-send-keys-hold-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    runHelper("seed-spawn", { store, state: "check_permission", id, "relay-mode": "on", "create-store": true,
      socket: privateTmuxSocket(homeDir) });
    // Past its relay window (fallen back), its hook gone a minute ago.
    const token = runHelper("seed-permission-request", { store, "spawn-id": id, tool: "Bash",
      "created-ago-seconds": "172800", "hook-gone-ago-seconds": "60" })["request_token"] as string;
    const paneId = runHelper("seed-row-session", { store, id, capture: "answered at tmux\n" })["pane_id"] as string;

    const got = await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      const { pane_sha256 } = await client.readPane({ claude_instance_id: id });
      await client.recordPaneAnswer({ request_token: token, as: "unknown", expect_pane_sha256: pane_sha256 });
      const err = (await rejection(client.sendKeys({ claude_instance_id: id, text: "hi" }))) as AgentDirectorError;
      const row = await client.get({ claude_instance_id: id });
      const perm = await client.getPermission({ request_token: token });
      const passed = await client.sendKeys({ claude_instance_id: id, text: "hi", expect_pane_sha256: pane_sha256 });
      return { err, row, perm, passed };
    });

    expect(got.err).toBeInstanceOf(ErrDialogMaybeOpen);
    const details = got.err.errDetails as unknown as DialogMaybeOpenDetails;
    expect([details.request_token, details.pane_answer, details.proven_gone_at, details.proven_gone_how, details.state,
      details.unproven_requests]).toEqual([token, "outside", null, null, "check_permission", []]);
    expect(details.unproven_since).not.toBeNull();
    expect(got.row.unproven_requests.map((r) => [r.request_token, r.unproven_since])).toEqual([[token, details.unproven_since]]);
    expect([got.perm.proven_gone_at, got.perm.proven_gone_how, got.perm.unproven_since]).toEqual([null, null, details.unproven_since]);
    expect(got.passed).toEqual({});
    const sends = fakeTmuxCalls(logPath)
      .filter((argv) => argv.includes("send-keys"))
      .map((argv) => argv.slice(argv.indexOf("send-keys")));
    expect(sends).toEqual([
      ["send-keys", "-t", paneId, "-l", "--", "hi"],
      ["send-keys", "-t", paneId, "Enter"],
    ]);
  });
}, 15_000);

test("send-keys: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.sendKeys({ claude_instance_id: "smoke-bogus-id", text: "hello" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
