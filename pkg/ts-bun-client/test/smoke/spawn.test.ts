/**
 * Smoke test — spawn verb
 *
 * Happy path: cwd = the temp HOME dir (already exists), with a temp-HOME
 * .claude.json lacking its trust entry. Asserts claude_instance_id and
 * pre_trust: ok writes the entry; no_pre_trust reports skipped and leaves the
 * file byte-identical (SR-22.6).
 *
 * Error paths: empty cwd → ErrCwdMissing; an id with a control character →
 * ErrInvalidFlags with no row and no tmux session created.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrCwdMissing, ErrInvalidFlags, AgentDirectorError } from "../../src/index.js";
import type { SpawnResult } from "../../src/index.js";

// The FFI worker inherits a snapshot of process.env at spawn time and does NOT
// see subsequent changes from withTempHome. Pass tmuxCommand explicitly so the
// worker uses fake-tmux regardless of what PATH looks like in the worker thread.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

// When running inside a Claude session, AGENT_DIRECTOR_INSTANCE_ID is set in the
// OS environment. The FFI worker (which always inherits the ORIGINAL OS env) reads
// it as parent_id for InsertPending. The test store must contain a parent row with
// that ID or the FOREIGN KEY constraint will fail. We seed it conditionally here.
const OUTER_INSTANCE_ID = process.env.AGENT_DIRECTOR_INSTANCE_ID;

/** Seeds the outer parent row (when set) so a successful spawn's FK holds. */
function seedOuterParent(storePath: string): void {
  if (!OUTER_INSTANCE_ID) return;
  runHelper("seed-spawn", {
    store: storePath,
    id: OUTER_INSTANCE_ID,
    state: "working",
    "create-store": true,
  });
}

/** Runs fn with FAKE_TMUX_LOG set to logPath (the client's CLI inherits process.env per call). */
async function withFakeTmuxLog(logPath: string, fn: () => Promise<void>): Promise<void> {
  const prior = process.env.FAKE_TMUX_LOG;
  process.env.FAKE_TMUX_LOG = logPath;
  try {
    await fn();
  } finally {
    if (prior !== undefined) process.env.FAKE_TMUX_LOG = prior;
    else delete process.env.FAKE_TMUX_LOG;
  }
}

/** Counts fake-tmux invocations whose argv includes new-session. */
function newSessionCount(logPath: string): number {
  if (!fs.existsSync(logPath)) return 0;
  return fs
    .readFileSync(logPath, "utf8")
    .split("---\n")
    .filter((rec) => rec.split("\n").includes("new-session")).length;
}

/** The temp HOME's .claude.json as planted before the spawn: no trust entry for any folder. */
const CLAUDE_JSON = '{"projects": {}}\n';

/** projects[cwd].hasTrustDialogAccepted in claudeJsonPath (undefined when absent). */
function trustEntry(claudeJsonPath: string, cwd: string): unknown {
  return JSON.parse(fs.readFileSync(claudeJsonPath, "utf8")).projects?.[cwd]?.hasTrustDialogAccepted;
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
      // sets parent_id = OUTER_INSTANCE_ID on the new spawn row.
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
test.each([
  ["newline", "\n"],
  ["tab", "\t"],
  ["DEL (0x7f)", "\x7f"],
])(
  "spawn: error — id containing %s → ErrInvalidFlags, no row, no session",
  async (_label, ctl) => {
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
          await client.spawn({ cwd: homeDir, claude_instance_id: badId });
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
