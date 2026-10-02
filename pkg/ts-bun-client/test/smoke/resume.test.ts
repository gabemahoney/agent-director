/**
 * Smoke test — resume verb
 *
 * Happy path: seed a spawn in ended state with claude_session_id set, write
 * the JSONL placeholder to disk, then call resume. The row records a socket
 * under the temp HOME; the fake-tmux stub creates the session on it.
 *
 * The subprocess CLI inherits HOME=homeDir (set by withTempHome), so the
 * JSONL pre-flight and pre-trust resolve against the temp HOME. We write the
 * JSONL and .claude.json under homeDir; cleanup is implicit via withTempHome.
 *
 * JSONL path formula (mirrors Go's spawn.JsonlPath):
 *   ${HOME}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl
 * where slug() replaces every non-[A-Za-z0-9-] rune with '-'. For cwd="/tmp":
 *   slug("/tmp") = "-tmp"
 *   path = ${homeDir}/.claude/projects/-tmp/${sessionId}.jsonl
 *
 * pre_trust (SR-22.6): the temp HOME holds a .claude.json lacking the cwd's
 * trust entry. An allowed row reports ok and gains the entry; a row seeded
 * with the opt-out (seed-spawn --no-pre-trust) reports skipped and leaves the
 * file byte-identical.
 *
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import { runHelper, privateTmuxSocket, CLAUDE_JSON, trustEntry, seedOuterParent } from "../internal/helper.js";
import { Client, ErrSpawnNotFound, AgentDirectorError } from "../../src/index.js";
import type { ResumeResult } from "../../src/index.js";

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

test.each([
  ["ok", "an allowed row", false],
  ["skipped", "an opted-out row", true],
] as const)(
  "resume: happy path — relaunches an ended spawn, pre_trust %s for %s",
  async (want, _label, noPreTrust) => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const spawnId = `smoke-resume-${crypto.randomUUID().slice(0, 8)}`;
      const cwd = "/tmp";
      const sessionId = `sess-${spawnId}`;
      const claudeJson = path.join(homeDir, ".claude.json");
      fs.writeFileSync(claudeJson, CLAUDE_JSON);

      // The resume records the outer agent's id as parent_id; its row must exist.
      seedOuterParent(storePath);

      // Seed a spawn in ended state with a claude_session_id set, recorded on
      // a socket whose directory exists (resume launches on that socket).
      runHelper("seed-spawn", {
        store: storePath,
        state: "ended",
        id: spawnId,
        cwd,
        "session-id": sessionId,
        "create-store": true,
        socket: privateTmuxSocket(homeDir),
        ...(noPreTrust ? { "no-pre-trust": true as const } : {}),
      });

      // Write the JSONL placeholder at the path the subprocess CLI's
      // os.UserHomeDir() resolves to (HOME=homeDir, inherited from withTempHome):
      // ${homeDir}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl
      const jsonlDir = path.join(homeDir, ".claude", "projects", slugifyCwd(cwd));
      const jsonlFile = path.join(jsonlDir, `${sessionId}.jsonl`);
      fs.mkdirSync(jsonlDir, { recursive: true });
      fs.writeFileSync(jsonlFile, "{}\n");

      using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);
      const result: ResumeResult = await client.resume({ claude_instance_id: spawnId });
      expect(result.claude_instance_id).toBe(spawnId);
      expect(result.pre_trust).toBe(want);
      if (noPreTrust) {
        expect(fs.readFileSync(claudeJson, "utf8")).toBe(CLAUDE_JSON);
      } else {
        expect(trustEntry(claudeJson, cwd)).toBe(true);
      }
    });
  },
  10_000,
);

test("resume: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    const storePath = path.join(homeDir, ".agent-director", "state.db");
    using client = await Client.create({ storePath, createIfMissing: true , _cliPath: process.env.CLI_PATH } as any);

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
