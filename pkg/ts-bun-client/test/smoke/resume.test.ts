/**
 * Smoke test — resume verb
 *
 * Happy path: an ended row with a claude_session_id, its JSONL placeholder at
 * ${HOME}/.claude/projects/${slug(cwd)}/${sessionId}.jsonl (Go's
 * spawn.JsonlPath; slug maps every non-[A-Za-z0-9-] rune to '-'), and a socket
 * under the temp HOME, where the fake tmux creates the session.
 * pre_trust (SR-22.6): the temp HOME's .claude.json lacks the cwd's entry; an
 * allowed row reports ok and gains it, a row seeded --no-pre-trust reports
 * skipped and leaves the file byte-identical.
 * Error path: unknown id → ErrSpawnNotFound.
 */

import { test, expect } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "../internal/tempHome.js";
import {
  runHelper, privateTmuxSocket, CLAUDE_JSON, trustEntry, seedOuterParent, homeStore, openClient,
} from "../internal/helper.js";
import { ErrSpawnNotFound } from "../../src/index.js";

test.each([
  ["ok", "an allowed row", false],
  ["skipped", "an opted-out row", true],
] as const)("resume: happy path — relaunches an ended spawn, pre_trust %s for %s", async (want, _label, noPreTrust) => {
  await withTempHome(async (homeDir) => {
    const store = homeStore(homeDir);
    const id = `smoke-resume-${crypto.randomUUID().slice(0, 8)}`;
    const cwd = "/tmp";
    const claudeJson = path.join(homeDir, ".claude.json");
    fs.writeFileSync(claudeJson, CLAUDE_JSON);
    seedOuterParent(store);
    runHelper("seed-spawn", {
      store, state: "ended", id, cwd, "session-id": `sess-${id}`, "create-store": true,
      socket: privateTmuxSocket(homeDir), ...(noPreTrust ? { "no-pre-trust": true as const } : {}),
    });
    const jsonlDir = path.join(homeDir, ".claude", "projects", cwd.replace(/[^A-Za-z0-9-]/g, "-"));
    fs.mkdirSync(jsonlDir, { recursive: true });
    fs.writeFileSync(path.join(jsonlDir, `sess-${id}.jsonl`), "{}\n");

    using client = await openClient(store);
    const result = await client.resume({ claude_instance_id: id });
    expect(result.claude_instance_id).toBe(id);
    expect(result.pre_trust).toBe(want);
    if (noPreTrust) expect(fs.readFileSync(claudeJson, "utf8")).toBe(CLAUDE_JSON);
    else expect(trustEntry(claudeJson, cwd)).toBe(true);
  });
}, 10_000);

test("resume: error — unknown id → ErrSpawnNotFound", async () => {
  await withTempHome(async (homeDir) => {
    using client = await openClient(homeStore(homeDir));
    await expect(client.resume({ claude_instance_id: "smoke-bogus-id" })).rejects.toBeInstanceOf(ErrSpawnNotFound);
  });
}, 10_000);
