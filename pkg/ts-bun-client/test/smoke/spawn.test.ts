/**
 * Smoke test — spawn verb
 *
 * Happy path: cwd = the temp HOME dir (already exists). Asserts result has
 * claude_instance_id field.
 *
 * Error path: empty cwd → ErrCwdMissing.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrCwdMissing, AgentDirectorError } from "../../src/index.js";
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

test("spawn: happy path — creates instance with valid cwd", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");

    // Pre-seed the parent row so the FK constraint is satisfied when the worker
    // sets parent_id = OUTER_INSTANCE_ID on the new spawn row.
    if (OUTER_INSTANCE_ID) {
      runHelper("seed-spawn", {
        store: storePath,
        id: OUTER_INSTANCE_ID,
        state: "working",
        "create-store": true,
      });
    }

    using client = await Client.create({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin , _cliPath: process.env.CLI_PATH } as any);
    const result: SpawnResult = await client.spawn({ cwd: homeDir });
    expect(typeof result.claude_instance_id).toBe("string");
    expect(result.claude_instance_id.length).toBeGreaterThan(0);
  });
}, 10_000);

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
