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
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, seedFallenBackRelayRow, privateTmuxSocket, fakeTmuxCalls, paneSends, withProcessEnv, homeStore, openClient, rejection } from "../internal/helper.js";
import { ErrPauseTimeout, ErrDialogMaybeOpen, ErrRelayFallenBack, ErrSpawnNotPausable, type PauseTimeoutDetails, type AgentDirectorError, type DialogMaybeOpenDetails,
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

test("pause: timeout preserves inspection details and sends no follow-up input", async () => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = "smoke-pause-timeout";
    const logPath = path.join(homeDir, "fake-tmux.log");
    const configPath = path.join(homeDir, ".config", "agent-director", "config.toml");
    runHelper("seed-spawn", { store, state: "waiting", id, "create-store": true, socket: privateTmuxSocket(homeDir) });
    fs.writeFileSync(configPath, "[pause]\ntimeout_seconds = 1\n");
    const paneId = runHelper("seed-row-session", { store, id, capture: "Background work is running\n1. Exit and stop tasks / 2. Stay\n" })["pane_id"] as string;

    const err = await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      using client = await openClient(store);
      return await rejection(client.pause({ instance_id: id })) as AgentDirectorError;
    });

    expect(err).toBeInstanceOf(ErrPauseTimeout);
    expect(err.verb).toBe("pause");
    const details = { inspection_required: true, exit_keys_sent: true, exit_confirmed: false, row_state: "waiting" } satisfies PauseTimeoutDetails;
    expect(err.errDetails).toEqual(details);
    expect(err.errDescription).toContain("do not retry pause automatically");
    expect(paneSends(logPath)).toEqual([
      ["send-keys", "-t", paneId, "C-u"],
      ["send-keys", "-t", paneId, "-l", "--", "/exit"],
      ["send-keys", "-t", paneId, "Enter"],
    ]);
    expect(fakeTmuxCalls(logPath).some((args) => args.includes("capture-pane"))).toBe(false);
  });
}, 10_000);
