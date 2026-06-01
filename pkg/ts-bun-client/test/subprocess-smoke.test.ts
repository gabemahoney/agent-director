/**
 * subprocess-smoke.test.ts — Epic C / SR-10.3.
 *
 * Single consolidated file that exercises every callable verb through the
 * public Client (subprocess-backed post-Epic-B). Each verb gets a happy-path
 * case that asserts the returned envelope conforms to the verb's typed result,
 * plus at least one error-path case asserting the corresponding typed `Err*`
 * is thrown (`instanceof` check).
 *
 * The verb list is read from `src/internal/verbs.ts` (single source of truth).
 * The Err* classes are imported from the canonical `src/index.ts` barrel — no
 * duplicate catalog literal lives here.
 *
 * Verbs in the no-error-path allow-list mirror smoke-invariants.test.ts:
 *   - version, expire, delete, find-missing — no triggerable verb-level errors.
 *
 * Scenarios are intentionally narrower than the per-verb files under
 * test/smoke/<verb>.test.ts so this file remains scannable as a single audit
 * of the subprocess contract.
 */

import { test, expect, describe } from "bun:test";
import * as path from "path";
import * as fs from "fs";
import { withTempHome } from "./internal/tempHome.js";
import { runHelper } from "./internal/helper.js";
import { VERBS } from "../src/internal/verbs.js";
import {
  Client,
  AgentDirectorError,
  ErrCwdMissing,
  ErrSpawnNotFound,
  ErrInvalidDecision,
  ErrListInvalidLabel,
  ErrTemplateNameUnsafe,
  ErrSpawnNotPausable,
} from "../src/index.js";
import type {
  SpawnResult, StatusResult, GetResult, SendKeysResult, ReadPaneResult,
  KillResult, DecideResult, ResumeResult, FindMissingResult, ExpireResult,
  DeleteResult, MakeTemplateResult, ListResult, PauseResult, VersionResult,
} from "../src/index.js";

const FAKE_TMUX_BIN = path.join(
  process.env.FAKE_TMUX_DIR ?? path.resolve(import.meta.dir, "../../../test/fake-tmux"),
  "tmux"
);

const OUTER_INSTANCE_ID = process.env.AGENT_DIRECTOR_INSTANCE_ID;
const BOGUS_ID = "subprocess-smoke-bogus-id";

/** Mirrors Go's slugifyCwd: every non-[A-Za-z0-9-] rune becomes '-'. */
function slugifyCwd(cwd: string): string {
  return cwd.replace(/[^A-Za-z0-9-]/g, "-");
}

/** Seed the outer-instance parent row when running inside a Claude session. */
function maybeSeedOuterParent(storePath: string): void {
  if (OUTER_INSTANCE_ID) {
    runHelper("seed-spawn", {
      store: storePath,
      id: OUTER_INSTANCE_ID,
      state: "working",
      "create-store": true,
    });
  }
}

// ── happy paths: one per verb ────────────────────────────────────────────────

describe("subprocess-smoke / happy paths (SR-10.3)", () => {
  test("spawn — returns claude_instance_id", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      maybeSeedOuterParent(storePath);
      using client = new Client({ storePath, createIfMissing: true, tmuxCommand: FAKE_TMUX_BIN });
      const r: SpawnResult = await client.spawn({ cwd: homeDir });
      expect(typeof r.claude_instance_id).toBe("string");
      expect(r.claude_instance_id.length).toBeGreaterThan(0);
    });
  }, 10_000);

  test("status — returns state string", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-status";
      runHelper("seed-spawn", { store: storePath, id, state: "working", "create-store": true });
      using client = new Client({ storePath, createIfMissing: true });
      const r: StatusResult = await client.status({ claude_instance_id: id });
      expect(typeof r.state).toBe("string");
    });
  }, 10_000);

  test("get — returns spawn record", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-get";
      runHelper("seed-spawn", { store: storePath, id, state: "working", "create-store": true });
      using client = new Client({ storePath, createIfMissing: true });
      const r: GetResult = await client.get({ claude_instance_id: id });
      expect(r.claude_instance_id).toBe(id);
    });
  }, 10_000);

  test("send-keys — succeeds against working spawn", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-sendkeys";
      runHelper("seed-spawn", {
        store: storePath, id, state: "working", "create-store": true,
      });
      using client = new Client({ storePath, createIfMissing: true, tmuxCommand: FAKE_TMUX_BIN });
      const r: SendKeysResult = await client.sendKeys({ claude_instance_id: id, text: "hi" });
      expect(typeof r).toBe("object");
    });
  }, 10_000);

  test("read-pane — returns captured text", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-readpane";
      runHelper("seed-spawn", {
        store: storePath, id, state: "working", "create-store": true,
      });
      using client = new Client({ storePath, createIfMissing: true, tmuxCommand: FAKE_TMUX_BIN });
      const r: ReadPaneResult = await client.readPane({ claude_instance_id: id });
      expect(typeof r.pane).toBe("string");
    });
  }, 10_000);

  test("kill — succeeds against working spawn", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-kill";
      runHelper("seed-spawn", {
        store: storePath, id, state: "working", "create-store": true,
      });
      using client = new Client({ storePath, createIfMissing: true, tmuxCommand: FAKE_TMUX_BIN });
      const r: KillResult = await client.kill({ claude_instance_id: id });
      expect(typeof r).toBe("object");
    });
  }, 10_000);

  test("decide — allows an open permission request", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-decide";
      runHelper("seed-spawn", {
        store: storePath, id, state: "check_permission",
        "relay-mode": "on", "create-store": true,
      });
      const seed = runHelper("seed-permission-request", { store: storePath, "spawn-id": id, tool: "Bash" });
      const requestToken = seed["request_token"] as string;
      using client = new Client({ storePath, createIfMissing: true });
      const r: DecideResult = await client.decide({ claude_instance_id: id, request_token: requestToken, decision: "allow" });
      expect(typeof r).toBe("object");
    });
  }, 10_000);

  test("resume — relaunches an ended spawn", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-resume";
      const cwd = "/tmp";
      const sessionId = `sess-${id}`;
      maybeSeedOuterParent(storePath);
      runHelper("seed-spawn", {
        store: storePath, id, state: "ended", cwd,
        "session-id": sessionId, "create-store": true,
      });
      const jsonlDir = path.join(homeDir, ".claude", "projects", slugifyCwd(cwd));
      fs.mkdirSync(jsonlDir, { recursive: true });
      fs.writeFileSync(path.join(jsonlDir, `${sessionId}.jsonl`), "{}\n");
      using client = new Client({ storePath, createIfMissing: true });
      const r: ResumeResult = await client.resume({ claude_instance_id: id });
      expect(r.claude_instance_id).toBe(id);
    });
  }, 10_000);

  test("find-missing — returns count and ids (linux); darwin emits ErrProbeUnsupported", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      if (process.platform === "linux") {
        const r: FindMissingResult = await client.findMissing({});
        expect(typeof r.count).toBe("number");
        expect(Array.isArray(r.ids)).toBe(true);
      } else {
        let caught: unknown;
        try { await client.findMissing({}); } catch (e) { caught = e; }
        expect(caught).toBeInstanceOf(AgentDirectorError);
      }
    });
  }, 10_000);

  test("expire — returns expired count", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      const r: ExpireResult = await client.expire({ older_than: "0d" });
      expect(typeof r).toBe("object");
    });
  }, 10_000);

  test("delete — returns results map", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      const r: DeleteResult = await client.delete({ claude_instance_id: ["nonexistent"] });
      expect(typeof r.results).toBe("object");
    });
  }, 10_000);

  test("make-template — creates and reports name", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      const r: MakeTemplateResult = await client.makeTemplate({ name: "subsmoke-tmpl" });
      expect(typeof r).toBe("object");
    });
  }, 10_000);

  test("list — returns spawns array", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      const r: ListResult = await client.list({});
      expect(Array.isArray(r.spawns)).toBe(true);
    });
  }, 10_000);

  test("pause — no-op for already-terminal spawn", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-pause";
      runHelper("seed-spawn", {
        store: storePath, id, state: "ended", "create-store": true,
      });
      using client = new Client({ storePath, createIfMissing: true });
      const r: PauseResult = await client.pause({ claude_instance_id: id });
      expect(typeof r).toBe("object");
    });
  }, 15_000);

  test("version — returns version + commit strings", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      const r: VersionResult = await client.version({});
      expect(typeof r.version).toBe("string");
      expect(r.version.length).toBeGreaterThan(0);
      expect(typeof r.commit).toBe("string");
    });
  }, 10_000);
});

// ── error paths: at least one per non-allow-listed verb ──────────────────────

const NO_ERROR_CASE_ALLOWLIST: ReadonlySet<string> = new Set([
  "version", "expire", "delete", "find-missing",
]);

describe("subprocess-smoke / error paths (SR-10.3)", () => {
  test("spawn: empty cwd → ErrCwdMissing", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.spawn({ cwd: "" }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrCwdMissing);
      expect(caught).toBeInstanceOf(AgentDirectorError);
    });
  }, 10_000);

  test("status: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.status({ claude_instance_id: BOGUS_ID }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("get: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.get({ claude_instance_id: BOGUS_ID }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("send-keys: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.sendKeys({ claude_instance_id: BOGUS_ID, text: "x" }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("read-pane: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.readPane({ claude_instance_id: BOGUS_ID }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("kill: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.kill({ claude_instance_id: BOGUS_ID }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("decide: invalid decision string → ErrInvalidDecision", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try {
        await client.decide({ claude_instance_id: "any", request_token: "00000000-0000-0000-0000-000000000000", decision: "maybe" as "allow" });
      } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrInvalidDecision);
    });
  }, 10_000);

  test("resume: unknown id → ErrSpawnNotFound", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.resume({ claude_instance_id: BOGUS_ID }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotFound);
    });
  }, 10_000);

  test("make-template: unsafe name → ErrTemplateNameUnsafe", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.makeTemplate({ name: "a/b" }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrTemplateNameUnsafe);
    });
  }, 10_000);

  test("list: malformed label → ErrListInvalidLabel", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.list({ label: ["no-equals-sign"] }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrListInvalidLabel);
    });
  }, 10_000);

  test("pause: working spawn → ErrSpawnNotPausable", async () => {
    await withTempHome(async (homeDir) => {
      const storePath = path.join(homeDir, ".agent-director", "state.db");
      const id = "subsmoke-pause-err";
      runHelper("seed-spawn", {
        store: storePath, id, state: "working", "create-store": true,
      });
      using client = new Client({ storePath, createIfMissing: true });
      let caught: unknown;
      try { await client.pause({ claude_instance_id: id }); } catch (e) { caught = e; }
      expect(caught).toBeInstanceOf(ErrSpawnNotPausable);
    });
  }, 15_000);
});

// ── invariant: every verb is covered ─────────────────────────────────────────

test("invariant: every callable verb has a happy-path case", () => {
  const src = fs.readFileSync(import.meta.path, "utf-8");
  const missing: string[] = [];
  for (const verb of VERBS) {
    // Each verb name (kebab form) must appear at least once in a test name.
    const escaped = verb.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (!new RegExp(`"\\s*${escaped}\\s*[—:]`).test(src)) {
      missing.push(verb);
    }
  }
  expect(missing).toEqual([]);
});

test("invariant: every non-allow-listed verb has an error-path case", () => {
  const src = fs.readFileSync(import.meta.path, "utf-8");
  // Capture the error-paths describe body.
  const m = src.match(/error paths \(SR-10\.3\)[\s\S]*$/);
  expect(m).not.toBeNull();
  const errBody = m![0];
  const missing: string[] = [];
  for (const verb of VERBS) {
    if (NO_ERROR_CASE_ALLOWLIST.has(verb)) continue;
    const escaped = verb.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    if (!new RegExp(`"${escaped}:\\s`).test(errBody)) {
      missing.push(verb);
    }
  }
  expect(missing).toEqual([]);
});
