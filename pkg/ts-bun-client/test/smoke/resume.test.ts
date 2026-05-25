/**
 * Smoke test — resume verb
 *
 * Happy path: seed a spawn in ended state with claude_session_id set, write
 * the JSONL placeholder to disk, then call resume. The fake-tmux stub handles
 * the new-session call.
 *
 * IMPORTANT — HOME isolation caveat:
 *   The FFI worker thread inherits a snapshot of process.env at spawn time and
 *   does NOT see the HOME override set by withTempHome in the main thread. Go's
 *   os.UserHomeDir() in the worker therefore returns the REAL HOME, not the temp
 *   dir. The JSONL pre-flight in resumeImpl uses that real HOME path. We capture
 *   the real HOME at module-load time (before any withTempHome call) and write
 *   the JSONL there so the os.Stat check passes. The file is deleted in a
 *   finally block to avoid leaving residue in ~/.claude/projects/.
 *
 * The JSONL path formula (mirrors Go's spawn.JsonlPath):
 *   ${HOME}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl
 * where slug() replaces every non-[A-Za-z0-9-] rune with '-'. For cwd="/tmp":
 *   slug("/tmp") = "-tmp"
 *   path = ${realHome}/.claude/projects/-tmp/${sessionId}.jsonl
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { ResumeResult } from "../../src/index.js";

// Capture the real HOME before any withTempHome call overrides process.env.HOME.
// The FFI worker uses this value (os.UserHomeDir() in Go) regardless of
// withTempHome's main-thread override.
const REAL_HOME = process.env.HOME ?? "/home/horde";

// Pass tmuxCommand explicitly — the FFI worker's PATH snapshot does not reflect
// changes made by withTempHome in the main thread after worker spawn.
const fakeTmuxBin = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../../test/fake-tmux"),
  "tmux"
);

// When running inside a Claude session, AGENT_DIRECTOR_INSTANCE_ID is set in the
// OS environment. The FFI worker reads it as parent_id for SetParentID. Seed the
// parent row in any resume test store so the FOREIGN KEY constraint is satisfied.
const OUTER_INSTANCE_ID = process.env.AGENT_DIRECTOR_INSTANCE_ID;

const BOGUS_ID = "smoke-bogus-id-does-not-exist";

/** Mirrors Go's slugifyCwd: every non-[A-Za-z0-9-] rune becomes '-'. */
function slugifyCwd(cwd: string): string {
  let out = "";
  for (const ch of cwd) {
    if (/[A-Za-z0-9-]/.test(ch)) {
      out += ch;
    } else {
      out += "-";
    }
  }
  return out;
}

test("resume: happy path — relaunches an ended spawn", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    const spawnId = "smoke-resume-id";
    const cwd = "/tmp";
    const sessionId = `sess-${spawnId}`;

    // Pre-seed the parent row so the FK constraint is satisfied when the worker
    // sets parent_id = OUTER_INSTANCE_ID via SetParentID in resumeImpl.
    if (OUTER_INSTANCE_ID) {
      runHelper("seed-spawn", {
        store: storePath,
        id: OUTER_INSTANCE_ID,
        state: "working",
        "create-store": true,
      });
    }

    // Seed a spawn in ended state with a claude_session_id set.
    runHelper("seed-spawn", {
      store: storePath,
      state: "ended",
      id: spawnId,
      cwd,
      "session-id": sessionId,
      "create-store": true,
    });

    // Write the JSONL placeholder at the path Go's os.UserHomeDir() resolves to
    // (the real HOME, not the per-test temp HOME). Path formula:
    //   ${REAL_HOME}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl
    const jsonlDir = path.join(REAL_HOME, ".claude", "projects", slugifyCwd(cwd));
    const jsonlFile = path.join(jsonlDir, `${sessionId}.jsonl`);
    fs.mkdirSync(jsonlDir, { recursive: true });
    fs.writeFileSync(jsonlFile, "{}\n");

    try {
      using client = new Client({ storePath, createIfMissing: true, tmuxCommand: fakeTmuxBin });
      const result: ResumeResult = await client.resume({ claude_instance_id: spawnId });
      expect(result.claude_instance_id).toBe(spawnId);
    } finally {
      // Clean up the JSONL file we wrote to the real HOME.
      try { fs.unlinkSync(jsonlFile); } catch { /* best-effort */ }
    }
  });
}, 10_000);

test("resume: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = new Client({ storePath, createIfMissing: true });

    let caught: unknown;
    try {
      await client.resume({ claude_instance_id: BOGUS_ID });
    } catch (e) {
      caught = e;
    }
    expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    expect(caught).toBeInstanceOf(AgentDirectorError);
    expect(caught).toBeInstanceOf(Error);
  });
}, 10_000);
