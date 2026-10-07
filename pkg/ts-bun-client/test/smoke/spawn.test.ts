/**
 * Smoke test — spawn verb
 *
 * Happy path: cwd = the temp HOME, whose .claude.json lacks its trust entry;
 * pre_trust ok writes the entry, no_pre_trust reports skipped and leaves the
 * file byte-identical (SR-22.6).
 * Errors: empty cwd → ErrCwdMissing; an id with a control character →
 * ErrInvalidFlags with no row and no session, with or without reuse_finished.
 * reuse_finished (SR-10.1, SR-10.2): without an id it mints one; with an id an
 * ended row is reused (pending, one session); without the opt-in an ended row
 * collides (ErrInstanceIdCollision), row unchanged, no session.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import {
  runHelper, privateTmuxSocket, fakeTmuxCalls, withProcessEnv, CLAUDE_JSON, trustEntry, seedOuterParent,
  homeStore, openClient, rejection,
} from "../internal/helper.js";
import { ErrCwdMissing, ErrInvalidFlags, ErrInstanceIdCollision, AgentDirectorError } from "../../src/index.js";

/** fake-tmux invocations in logPath whose argv includes new-session. */
const newSessionCount = (logPath: string) => fakeTmuxCalls(logPath).filter((argv) => argv.includes("new-session")).length;

test.each([
  ["ok", false],
  ["skipped", true],
] as const)("spawn: happy path — creates instance with valid cwd, pre_trust %s", async (want, noPreTrust) => {
  await withTempHome(async (homeDir) => {
    const claudeJson = path.join(homeDir, ".claude.json");
    fs.writeFileSync(claudeJson, CLAUDE_JSON);
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const result = await client.spawn({ cwd: homeDir, ...(noPreTrust ? { no_pre_trust: true } : {}) });
    expect(result.claude_instance_id.length).toBeGreaterThan(0);
    expect(result.pre_trust).toBe(want);
    if (noPreTrust) expect(fs.readFileSync(claudeJson, "utf8")).toBe(CLAUDE_JSON);
    // The entry is keyed by the canonical cwd (the spawn resolves symlinks).
    else expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
  });
}, 10_000);

test("spawn: error — empty cwd → ErrCwdMissing", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.spawn({ cwd: "" })).rejects.toBeInstanceOf(ErrCwdMissing);
  });
}, 10_000);

// SR-9.1 / AC-SPN-02: the UUID-suffixed prefix cannot collide across runs, and the
// message is checked for any echo of it. The reuse_finished row: the opt-in does not bypass the check (SR-10.1).
test.each([
  ["newline", "\n", false],
  ["tab", "\t", false],
  ["DEL (0x7f)", "\x7f", false],
  ["newline with reuse_finished", "\n", true],
] as const)("spawn: error — id containing %s → ErrInvalidFlags, no row, no session", async (_label, ctl, reuse) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const prefix = `ctl-id-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      const before = (await client.list({})).spawns.length;
      const err = await rejection(
        client.spawn({ cwd: homeDir, claude_instance_id: `${prefix}${ctl}x`, ...(reuse ? { reuse_finished: true } : {}) })
      );
      expect(err).toBeInstanceOf(ErrInvalidFlags);
      expect((err as AgentDirectorError).message).toContain("the instance id contains a control character");
      expect((err as AgentDirectorError).message).not.toContain(prefix);
      const after = (await client.list({})).spawns;
      expect(after.length).toBe(before);
      expect(after.some((r) => r.claude_instance_id.startsWith(prefix))).toBe(false);
      expect(newSessionCount(logPath)).toBe(0);

      // Control: a clean id in the same wiring records new-session (the log is routed).
      expect((await client.spawn({ cwd: homeDir, claude_instance_id: `${prefix}-ok` })).claude_instance_id).toBe(`${prefix}-ok`);
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 20_000);

// SR-10.1: reuse_finished has no effect without an explicit id; the spawn mints one.
test("spawn: reuse_finished without claude_instance_id → fresh spawn with a minted id", async () => {
  await withTempHome(async (homeDir) => {
    seedOuterParent(homeStore(homeDir));
    using client = await openClient(homeStore(homeDir));
    const result = await client.spawn({ cwd: homeDir, reuse_finished: true });
    expect((await client.get({ claude_instance_id: result.claude_instance_id })).state).toBe("pending");
  });
}, 10_000);

// SR-10.2: with the opt-in an ended row whose session is gone is reused: one new session, row pending.
test("spawn: reuse_finished over an ended row → reused, get shows pending", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true, socket: privateTmuxSocket(homeDir) });
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      expect((await client.spawn({ cwd: homeDir, claude_instance_id: id, reuse_finished: true })).claude_instance_id).toBe(id);
      expect((await client.get({ claude_instance_id: id })).state).toBe("pending");
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 10_000);

// AC-REUSE-13: without the opt-in an ended row still collides; the row is unchanged and no session is created.
test.each([
  ["absent", {}],
  ["false", { reuse_finished: false }],
] as const)("spawn: error — ended row, reuse_finished %s → ErrInstanceIdCollision", async (_label, extra) => {
  await withTempHome(async (homeDir) => {
    const storePath = homeStore(homeDir);
    const logPath = path.join(homeDir, "fake-tmux.log");
    const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true });
    using client = await openClient(storePath);
    await withProcessEnv({ FAKE_TMUX_LOG: logPath }, async () => {
      const before = await client.get({ claude_instance_id: id });
      expect(before.state).toBe("ended");
      await expect(client.spawn({ cwd: homeDir, claude_instance_id: id, ...extra })).rejects.toBeInstanceOf(ErrInstanceIdCollision);
      expect(await client.get({ claude_instance_id: id })).toEqual(before);
      expect(newSessionCount(logPath)).toBe(0);

      // Control: a fresh id in the same wiring records new-session.
      await client.spawn({ cwd: homeDir, claude_instance_id: `${id}-ok` });
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 20_000);
