/**
 * Smoke test — spawn verb
 *
 * Happy path: cwd = the temp HOME dir (already exists), with a temp-HOME
 * .claude.json lacking its trust entry. Asserts claude_instance_id and
 * pre_trust: ok writes the entry; no_pre_trust reports skipped and leaves the
 * file byte-identical (SR-22.6).
 *
 * Error paths: empty cwd → ErrCwdMissing; an id with a control character →
 * ErrInvalidFlags with no row and no tmux session created, with or without
 * reuse_finished.
 *
 * reuse_finished (SR-10.1, SR-10.2): without an id it mints one; with an id an
 * ended row is reused (pending, one session); without the opt-in an ended row
 * collides (ErrInstanceIdCollision), row unchanged, no session.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import {
  runHelper,
  privateTmuxSocket,
  fakeTmuxCalls,
  withProcessEnv,
  CLAUDE_JSON,
  trustEntry,
  seedOuterParent,
} from "../internal/helper.js";
import { Client, ErrCwdMissing, ErrInvalidFlags, ErrInstanceIdCollision, AgentDirectorError } from "../../src/index.js";
import type { SpawnResult } from "../../src/index.js";

// The FFI worker inherits a snapshot of process.env at spawn time and does NOT
// see subsequent changes from withTempHome. Pass tmuxCommand explicitly so the
// worker uses fake-tmux regardless of what PATH looks like in the worker thread.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

/** Runs fn with FAKE_TMUX_LOG set to logPath (the client's CLI inherits process.env per call). */
async function withFakeTmuxLog(logPath: string, fn: () => Promise<void>): Promise<void> {
  await withProcessEnv({ FAKE_TMUX_LOG: logPath }, fn);
}

/** Counts fake-tmux invocations whose argv includes new-session. */
function newSessionCount(logPath: string): number {
  return fakeTmuxCalls(logPath).filter((argv) => argv.includes("new-session")).length;
}

test.each([
  ["ok", false],
  ["skipped", true],
] as const)(
  "spawn: happy path — creates instance with valid cwd, pre_trust %s",
  async (want, noPreTrust) => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const claudeJson = path.join(homeDir, ".claude.json");
      fs.writeFileSync(claudeJson, CLAUDE_JSON);

      // Pre-seed the parent row so the FK constraint is satisfied when the worker
      // sets parent_id = the outer agent's id on the new spawn row.
      seedOuterParent(storePath);

      using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin , _cliPath: process.env.CLI_PATH } as any);
      const result: SpawnResult = await client.spawn({ cwd: homeDir, ...(noPreTrust ? { no_pre_trust: true } : {}) });
      expect(typeof result.claude_instance_id).toBe("string");
      expect(result.claude_instance_id.length).toBeGreaterThan(0);
      expect(result.pre_trust).toBe(want);
      if (noPreTrust) {
        expect(fs.readFileSync(claudeJson, "utf8")).toBe(CLAUDE_JSON);
      } else {
        // The entry is keyed by the canonical cwd (the spawn resolves symlinks).
        expect(trustEntry(claudeJson, fs.realpathSync(homeDir))).toBe(true);
      }
    });
  },
  10_000,
);

test("spawn: error — empty cwd → ErrCwdMissing", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
    await expect(
      client.spawn({ cwd: "" })
    ).rejects.toBeInstanceOf(ErrCwdMissing);

    // Also assert the full inheritance chain.
    let caught: unknown;
    try {
      await client.spawn({ cwd: "" });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrCwdMissing);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);

// SR-9.1 / AC-SPN-02: the printable prefix is UUID-suffixed so a leaked session
// cannot collide across runs, and so the message can be checked for any echo of the id.
// The reuse_finished rows prove the opt-in does not bypass the check (SR-10.1).
test.each([
  ["newline", "\n", false],
  ["tab", "\t", false],
  ["DEL (0x7f)", "\x7f", false],
  ["newline with reuse_finished", "\n", true],
] as const)(
  "spawn: error — id containing %s → ErrInvalidFlags, no row, no session",
  async (_label, ctl, reuse) => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const logPath = path.join(homeDir, "fake-tmux.log");
      const prefix = `ctl-id-${crypto.randomUUID().slice(0, 8)}`;
      const badId = `${prefix}${ctl}x`;
      const goodId = `${prefix}-ok`;
      seedOuterParent(storePath);

      using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin, _cliPath: process.env.CLI_PATH } as any);
      await withFakeTmuxLog(logPath, async () => {
        const before = (await client.list({})).spawns.length;

        let caught: unknown;
        try {
          await client.spawn({ cwd: homeDir, claude_instance_id: badId, ...(reuse ? { reuse_finished: true } : {}) });
        } catch (e) {
          caught = e;
        }
        expect(caught).toBeInstanceOf(ErrInvalidFlags);
        expect(caught).toBeInstanceOf(AgentDirectorError);
        const err = caught as AgentDirectorError;
        expect(err.errName).toBe("ErrInvalidFlags");
        expect(err.message).toContain("the instance id contains a control character");
        expect(err.message).not.toContain(prefix);

        const after = (await client.list({})).spawns;
        expect(after.length).toBe(before);
        expect(after.some((r) => r.claude_instance_id.startsWith(prefix))).toBe(false);
        expect(newSessionCount(logPath)).toBe(0);

        // Control: a clean id in the same wiring does record new-session, so the
        // zero above is not an artefact of an unrouted FAKE_TMUX_LOG.
        const ok = await client.spawn({ cwd: homeDir, claude_instance_id: goodId });
        expect(ok.claude_instance_id).toBe(goodId);
        expect(newSessionCount(logPath)).toBe(1);
      });
    });
  },
  20_000,
);

// SR-10.1: reuse_finished has no effect without an explicit id; the spawn mints one.
test("spawn: reuse_finished without claude_instance_id → fresh spawn with a minted id", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    seedOuterParent(storePath);
    using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin, _cliPath: process.env.CLI_PATH } as any);
    const result = await client.spawn({ cwd: homeDir, reuse_finished: true });
    expect(result.claude_instance_id.length).toBeGreaterThan(0);
    expect((await client.get({ claude_instance_id: result.claude_instance_id })).state).toBe("pending");
  });
}, 10_000);

// SR-10.2: with the opt-in an ended row whose session is gone is reused: one new session, row pending.
test("spawn: reuse_finished over an ended row → reused, get shows pending", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const logPath = path.join(homeDir, "fake-tmux.log");
    const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
    seedOuterParent(storePath);
    runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true, socket: privateTmuxSocket(homeDir) });

    using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin, _cliPath: process.env.CLI_PATH } as any);
    await withFakeTmuxLog(logPath, async () => {
      const result = await client.spawn({ cwd: homeDir, claude_instance_id: id, reuse_finished: true });
      expect(result.claude_instance_id).toBe(id);
      expect((await client.get({ claude_instance_id: id })).state).toBe("pending");
      expect(newSessionCount(logPath)).toBe(1);
    });
  });
}, 10_000);

// AC-REUSE-13: without the opt-in an ended row still collides; the row is unchanged and no session is created.
test.each([
  ["absent", {}],
  ["false", { reuse_finished: false }],
] as const)(
  "spawn: error — ended row, reuse_finished %s → ErrInstanceIdCollision",
  async (_label, extra) => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const logPath = path.join(homeDir, "fake-tmux.log");
      const id = `reuse-ended-${crypto.randomUUID().slice(0, 8)}`;
      seedOuterParent(storePath);
      runHelper("seed-spawn", { store: storePath, id, state: "ended", "create-store": true });

      using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin, _cliPath: process.env.CLI_PATH } as any);
      await withFakeTmuxLog(logPath, async () => {
        const before = await client.get({ claude_instance_id: id });
        expect(before.state).toBe("ended");

        let caught: unknown;
        try {
          await client.spawn({ cwd: homeDir, claude_instance_id: id, ...extra });
        } catch (e) {
          caught = e;
        }
        expect(caught).toBeInstanceOf(ErrInstanceIdCollision);
        expect((caught as AgentDirectorError).errName).toBe("ErrInstanceIdCollision");
        expect(await client.get({ claude_instance_id: id })).toEqual(before);
        expect(newSessionCount(logPath)).toBe(0);

        // Control: a fresh id in the same wiring records new-session.
        await client.spawn({ cwd: homeDir, claude_instance_id: `${id}-ok` });
        expect(newSessionCount(logPath)).toBe(1);
      });
    });
  },
  20_000,
);
