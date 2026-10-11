/**
 * Smoke test — pause verb
 *
 * Happy path: an ended row is already terminal, so pause is a no-op success
 * with no tmux call. Error paths: a working row → ErrSpawnNotPausable; on a
 * relay-on waiting row pause is held as a plain sendKeys (b.zdz):
 * ErrRelayFallenBack, then ErrDialogMaybeOpen, typing nothing, which sendKeys
 * `/exit` with the pane's hash passes.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, seedFallenBackRelayRow, paneSends, withProcessEnv, homeStore, openClient, rejection } from "../internal/helper.js";
import { ErrDialogMaybeOpen, ErrRelayFallenBack, ErrSpawnNotPausable, type AgentDirectorError, type DialogMaybeOpenDetails,
  type RelayFallenBackDetails } from "../../src/index.js";

test("pause: happy path — no-op for already-terminal spawn", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "ended", id: "smoke-pause-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    expect(await client.pause({ instance_id: "smoke-pause-id" })).toEqual({});
  });
}, 10_000);

test("pause: error — working spawn is not pausable → ErrSpawnNotPausable", async () => {
  await withTempHome(async (homeDir) => {
    runHelper("seed-spawn", { store: homeStore(homeDir), state: "working", id: "smoke-pause-err-id", "create-store": true });
    using client = await openClient(homeStore(homeDir));
    await expect(client.pause({ instance_id: "smoke-pause-err-id" })).rejects.toBeInstanceOf(ErrSpawnNotPausable);
  });
}, 10_000);

test("pause: error — held as a plain sendKeys: ErrRelayFallenBack, then ErrDialogMaybeOpen; sendKeys `/exit` with the pane's hash passes", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-pause-hold-id";
    const logPath = path.join(homeDir, "fake-tmux.log");
    const { token, paneId } = seedFallenBackRelayRow(homeDir, id, "waiting");

    const got = await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      const fallenBack = (await rejection(client.pause({ instance_id: id }))) as AgentDirectorError;
      const { pane_sha256 } = await client.readPane({ instance_id: id });
      await client.recordPaneAnswer({ request_token: token, as: "unknown", expect_pane_sha256: pane_sha256 });
      const held = (await rejection(client.pause({ instance_id: id }))) as AgentDirectorError;
      const passed = await client.sendKeys({ instance_id: id, text: "/exit", expect_pane_sha256: pane_sha256 });
      return { fallenBack, held, passed };
    });

    expect(got.fallenBack).toBeInstanceOf(ErrRelayFallenBack);
    const fb = got.fallenBack.errDetails as unknown as RelayFallenBackDetails;
    expect([fb.request_token, fb.delivery, fb.state]).toEqual([token, "fallen_back", "waiting"]);
    expect(got.held).toBeInstanceOf(ErrDialogMaybeOpen);
    const details = got.held.errDetails as unknown as DialogMaybeOpenDetails;
    expect([details.request_token, details.pane_answer, details.proven_gone_at, details.state, details.unproven_requests])
      .toEqual([token, "outside", null, "waiting", []]);
    expect(got.passed).toEqual({});
    expect(paneSends(logPath)).toEqual([
      ["send-keys", "-t", paneId, "-l", "--", "/exit"],
      ["send-keys", "-t", paneId, "Enter"],
    ]);
  });
}, 15_000);
